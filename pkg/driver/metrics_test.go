/*
Copyright The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package driver

import (
	"context"
	"fmt"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	cpumetrics "github.com/kubernetes-sigs/dra-driver-cpu/pkg/metrics"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/utils/cpuset"
)

func newMetricsTestDriver(t *testing.T) (*CPUDriver, *prometheus.Registry) {
	t.Helper()
	logger := testr.New(t)
	mockProvider := &cpuinfo.MockCPUInfoProvider{CPUInfos: mockCPUInfos_SingleSocket_4CPUS_HT}
	topo, err := mockProvider.GetCPUTopology(logger)
	require.NoError(t, err)

	reg := prometheus.NewRegistry()
	recorder := cpumetrics.New(reg)
	cpuStore := store.NewCPUAllocation(topo, cpuset.New())
	driver := &CPUDriver{
		driverName: testDriverName,
		deviceNameToCPUID: map[string]int{
			"cpudev0": 0,
			"cpudev1": 1,
			"cpudev2": 2,
			"cpudev3": 3,
		},
		cpuAllocationStore: cpuStore,
		cdiMgr:             newMockCdiMgr(),
		metrics:            recorder,
	}
	driver.refreshAllocationMetrics()
	return driver, reg
}

func individualMetricsClaim(uid types.UID, devices ...string) *resourceapi.ResourceClaim {
	results := make([]resourceapi.DeviceRequestAllocationResult, 0, len(devices))
	for _, device := range devices {
		results = append(results, resourceapi.DeviceRequestAllocationResult{
			Driver: testDriverName,
			Pool:   testNodeName,
			Device: device,
		})
	}
	return &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{UID: uid, Name: string(uid)},
		Status: resourceapi.ResourceClaimStatus{
			Allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{Results: results},
			},
		},
	}
}

func metricValue(t *testing.T, reg *prometheus.Registry, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		for _, metric := range family.Metric {
			if metricLabelsMatch(metric, labels) {
				return metricSampleValue(metric)
			}
		}
	}
	require.FailNow(t, fmt.Sprintf("metric %s with labels %v not found", name, labels))
	return 0
}

func metricLabelsMatch(metric *dto.Metric, want map[string]string) bool {
	if len(metric.Label) != len(want) {
		return false
	}
	for _, label := range metric.Label {
		if want[label.GetName()] != label.GetValue() {
			return false
		}
	}
	return true
}

func metricSampleValue(metric *dto.Metric) float64 {
	switch {
	case metric.Gauge != nil:
		return metric.Gauge.GetValue()
	case metric.Counter != nil:
		return metric.Counter.GetValue()
	case metric.Histogram != nil:
		return float64(metric.Histogram.GetSampleCount())
	default:
		return 0
	}
}

func TestMetricsPrepareResults(t *testing.T) {
	driver, reg := newMetricsTestDriver(t)

	prepared, err := driver.PrepareResourceClaims(context.Background(), []*resourceapi.ResourceClaim{
		individualMetricsClaim("claim-success", "cpudev0"),
		{ObjectMeta: metav1.ObjectMeta{UID: "claim-error", Name: "claim-error"}},
	})
	require.NoError(t, err)
	require.NoError(t, prepared["claim-success"].Err)
	require.Error(t, prepared["claim-error"].Err)

	require.Equal(t, float64(1), metricValue(t, reg, "dra_cpu_prepare_claims_total", map[string]string{"result": cpumetrics.ResultSuccess}))
	require.Equal(t, float64(1), metricValue(t, reg, "dra_cpu_prepare_claims_total", map[string]string{"result": cpumetrics.ResultError}))
	require.Equal(t, float64(2), metricValue(t, reg, "dra_cpu_prepare_claim_duration_seconds", nil))
}

func TestMetricsAllocationStateAndClaimSize(t *testing.T) {
	driver, reg := newMetricsTestDriver(t)

	prepared, err := driver.PrepareResourceClaims(context.Background(), []*resourceapi.ResourceClaim{
		individualMetricsClaim("claim-alloc", "cpudev0", "cpudev1"),
	})
	require.NoError(t, err)
	require.NoError(t, prepared["claim-alloc"].Err)

	require.Equal(t, float64(2), metricValue(t, reg, "dra_cpu_allocated_cpus", nil))
	require.Equal(t, float64(2), metricValue(t, reg, "dra_cpu_available_cpus", nil))
	require.Equal(t, float64(0), metricValue(t, reg, "dra_cpu_reserved_cpus", nil))
	require.Equal(t, float64(1), metricValue(t, reg, "dra_cpu_resource_claims_active", nil))
	require.Equal(t, float64(1), metricValue(t, reg, "dra_cpu_claim_allocated_cpus", nil))

	prepared, err = driver.PrepareResourceClaims(context.Background(), []*resourceapi.ResourceClaim{
		individualMetricsClaim("claim-alloc", "cpudev0", "cpudev1"),
	})
	require.NoError(t, err)
	require.NoError(t, prepared["claim-alloc"].Err)
	require.Equal(t, float64(1), metricValue(t, reg, "dra_cpu_claim_allocated_cpus", nil), "duplicate prepare must not record a new allocation size")
}

func TestMetricsUnprepareResults(t *testing.T) {
	driver, reg := newMetricsTestDriver(t)
	claimUID := types.UID("claim-unprepare")
	prepared, err := driver.PrepareResourceClaims(context.Background(), []*resourceapi.ResourceClaim{
		individualMetricsClaim(claimUID, "cpudev0", "cpudev1"),
	})
	require.NoError(t, err)
	require.NoError(t, prepared[claimUID].Err)

	unprepared, err := driver.UnprepareResourceClaims(context.Background(), []kubeletplugin.NamespacedObject{{UID: claimUID}})
	require.NoError(t, err)
	require.NoError(t, unprepared[claimUID])
	require.Equal(t, float64(1), metricValue(t, reg, "dra_cpu_unprepare_claims_total", map[string]string{"result": cpumetrics.ResultSuccess}))
	require.Equal(t, float64(0), metricValue(t, reg, "dra_cpu_allocated_cpus", nil))
	require.Equal(t, float64(4), metricValue(t, reg, "dra_cpu_available_cpus", nil))
	require.Equal(t, float64(0), metricValue(t, reg, "dra_cpu_resource_claims_active", nil))

	driver.cdiMgr.(*mockCdiMgr).removeError = fmt.Errorf("remove failed")
	unprepared, err = driver.UnprepareResourceClaims(context.Background(), []kubeletplugin.NamespacedObject{{UID: "claim-error"}})
	require.NoError(t, err)
	require.Error(t, unprepared["claim-error"])
	require.Equal(t, float64(1), metricValue(t, reg, "dra_cpu_unprepare_claims_total", map[string]string{"result": cpumetrics.ResultError}))
}
