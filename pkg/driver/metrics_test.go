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

	"github.com/containerd/nri/pkg/api"
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
		topology: deviceTopology{
			cpuTopology: topo,
			deviceNameToCPUID: map[string]int{
				"cpudev0": 0,
				"cpudev1": 1,
				"cpudev2": 2,
				"cpudev3": 3,
			},
		},
		podConfigStore:     store.NewPodConfig(),
		cpuAllocationStore: cpuStore,
		claimTracker:       store.NewClaimTracker(),
		cdiMgr:             newMockCdiMgr(),
		metrics:            recorder,
	}
	driver.refreshAllocationMetrics()
	return driver, reg
}

type workload struct {
	claims []*resourceapi.ResourceClaim
	pod    *api.PodSandbox
	ctrs   []*api.Container
}

// keep these physically close to newMetricsTestDeiver because of the hidden
// dependency on the machine topology we use to define the minimal test workloads.
var minimalWorkloads = []workload{
	{
		claims: []*resourceapi.ResourceClaim{
			individualMetricsClaim(types.UID("app-0"), "cpudev1"),
		},
		pod: &api.PodSandbox{Id: "sandbox-0", Uid: "pod-uid-0", Name: "pod-0"},
		ctrs: []*api.Container{
			{
				Id: "container-0", PodSandboxId: "sandbox-0", Name: "app",
				Env: []string{fmt.Sprintf("%s_%s=%s", cdiEnvVarPrefix, "app-0", "1")},
			},
		},
	},
	{
		claims: []*resourceapi.ResourceClaim{
			individualMetricsClaim(types.UID("app-1"), "cpudev2", "cpudev3"),
		},
		pod: &api.PodSandbox{Id: "sandbox-1", Uid: "pod-uid-1", Name: "pod-1"},
		ctrs: []*api.Container{
			{
				Id: "container-1", PodSandboxId: "sandbox-1", Name: "app-1",
				Env: []string{fmt.Sprintf("%s_%s=%s", cdiEnvVarPrefix, "app-1", "2,3")},
			},
			{
				Id: "sidecar-1", PodSandboxId: "sandbox-1", Name: "sidecar-1",
			},
		},
	},
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

	require.Equal(t, float64(1), metricValue(t, reg, "dra_cpu_prepare_claims_total", map[string]string{"result": cpumetrics.ResultSuccess.String()}))
	require.Equal(t, float64(1), metricValue(t, reg, "dra_cpu_prepare_claims_total", map[string]string{"result": cpumetrics.ResultError.String()}))
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

func TestMetricsNRIAllocationState(t *testing.T) {
	driver, reg := newMetricsTestDriver(t)
	claimUID := types.UID("claim-nri")
	claimCPUs := cpuset.New(0, 1)
	driver.cdiMgr = newMockCdiMgrWithAllocations(map[types.UID]cpuset.CPUSet{
		claimUID: claimCPUs,
	})
	pod := &api.PodSandbox{Id: "sandbox", Uid: "pod-uid"}
	container := &api.Container{
		Id: "container", PodSandboxId: pod.Id, Name: "app",
		Env: []string{fmt.Sprintf("%s_%s=%s", cdiEnvVarPrefix, claimUID, claimCPUs.String())},
	}

	_, err := driver.Synchronize(context.Background(), []*api.PodSandbox{pod}, []*api.Container{container})
	require.NoError(t, err)
	require.Equal(t, float64(2), metricValue(t, reg, "dra_cpu_allocated_cpus", nil))
	require.Equal(t, float64(2), metricValue(t, reg, "dra_cpu_available_cpus", nil))
	require.Equal(t, float64(1), metricValue(t, reg, "dra_cpu_resource_claims_active", nil))

	_, err = driver.StopContainer(context.Background(), pod, container)
	require.NoError(t, err)
	require.Equal(t, float64(2), metricValue(t, reg, "dra_cpu_allocated_cpus", nil))
	require.Equal(t, float64(2), metricValue(t, reg, "dra_cpu_available_cpus", nil))
	require.Equal(t, float64(1), metricValue(t, reg, "dra_cpu_resource_claims_active", nil))
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
	require.Equal(t, float64(1), metricValue(t, reg, "dra_cpu_unprepare_claims_total", map[string]string{"result": cpumetrics.ResultSuccess.String()}))
	require.Equal(t, float64(0), metricValue(t, reg, "dra_cpu_allocated_cpus", nil))
	require.Equal(t, float64(4), metricValue(t, reg, "dra_cpu_available_cpus", nil))
	require.Equal(t, float64(0), metricValue(t, reg, "dra_cpu_resource_claims_active", nil))

	driver.cdiMgr.(*mockCdiMgr).removeError = fmt.Errorf("remove failed")
	unprepared, err = driver.UnprepareResourceClaims(context.Background(), []kubeletplugin.NamespacedObject{{UID: "claim-error"}})
	require.NoError(t, err)
	require.Error(t, unprepared["claim-error"])
	require.Equal(t, float64(1), metricValue(t, reg, "dra_cpu_unprepare_claims_total", map[string]string{"result": cpumetrics.ResultError.String()}))
	require.Equal(t, float64(2), metricValue(t, reg, "dra_cpu_unprepare_claim_duration_seconds", nil))
}

func TestMetricsNRISynchronizeSuccess(t *testing.T) {
	driver, reg := newMetricsTestDriver(t)
	driver.cdiMgr = newMockCdiMgrWithAllocations(map[types.UID]cpuset.CPUSet{
		"app-0": cpuset.New(1),
		"app-1": cpuset.New(2, 3),
	})

	pods := []*api.PodSandbox{
		{Id: "sandbox-0", Uid: "pod-uid-0", Name: "pod-0"},
		{Id: "sandbox-1", Uid: "pod-uid-1", Name: "pod-1"},
	}
	containers := []*api.Container{
		{
			Id: "container-0", PodSandboxId: "sandbox-0", Name: "app",
			Env: []string{fmt.Sprintf("%s_%s=%s", cdiEnvVarPrefix, "app-0", "1")},
		},
		{
			Id: "container-1", PodSandboxId: "sandbox-1", Name: "app-1",
			Env: []string{fmt.Sprintf("%s_%s=%s", cdiEnvVarPrefix, "app-1", "2,3")},
		},
		{
			Id: "sidecar-1", PodSandboxId: "sandbox-1", Name: "sidecar-1",
		},
	}
	_, err := driver.Synchronize(context.Background(), pods, containers)
	require.NoError(t, err)
	require.Equal(t, float64(1), metricValue(t, reg, "dra_cpu_nri_synchronize_duration_seconds", map[string]string{"result": cpumetrics.ResultSuccess.String()}))
	require.Equal(t, float64(0), metricValue(t, reg, "dra_cpu_nri_synchronize_duration_seconds", map[string]string{"result": cpumetrics.ResultError.String()}))
}

func TestMetricsNRISynchronizeFailure(t *testing.T) {
	driver, reg := newMetricsTestDriver(t)
	// intentionally exhausting the shared pool CPUs is both the simplest and
	// a realistic scenario to trigger and verify a Synchronize failure.
	driver.cdiMgr = newMockCdiMgrWithAllocations(map[types.UID]cpuset.CPUSet{
		"app-0": cpuset.New(0, 1),
		"app-1": cpuset.New(2, 3),
	})

	pods := []*api.PodSandbox{
		{Id: "sandbox-0", Uid: "pod-uid-0", Name: "pod-0"},
		{Id: "sandbox-1", Uid: "pod-uid-1", Name: "pod-1"},
	}
	containers := []*api.Container{
		{
			Id: "container-0", PodSandboxId: "sandbox-0", Name: "app",
			Env: []string{fmt.Sprintf("%s_%s=%s", cdiEnvVarPrefix, "app-0", "0,1")},
		},
		{
			Id: "container-1", PodSandboxId: "sandbox-1", Name: "app-1",
			Env: []string{fmt.Sprintf("%s_%s=%s", cdiEnvVarPrefix, "app-1", "2,3")},
		},
		{
			Id: "sidecar-1", PodSandboxId: "sandbox-1", Name: "sidecar-1",
		},
	}
	_, err := driver.Synchronize(context.Background(), pods, containers)
	require.Error(t, err)
	require.Equal(t, float64(0), metricValue(t, reg, "dra_cpu_nri_synchronize_duration_seconds", map[string]string{"result": cpumetrics.ResultSuccess.String()}))
	require.Equal(t, float64(1), metricValue(t, reg, "dra_cpu_nri_synchronize_duration_seconds", map[string]string{"result": cpumetrics.ResultError.String()}))
}

func TestMetricsNRICreateContainerSuccess(t *testing.T) {
	driver, reg := newMetricsTestDriver(t)

	for _, workload := range minimalWorkloads {
		_, err := driver.PrepareResourceClaims(context.Background(), workload.claims)
		require.NoError(t, err)

		for _, ctr := range workload.ctrs {
			_, _, err := driver.CreateContainer(context.Background(), workload.pod, ctr)
			require.NoError(t, err)
		}
	}
	for _, alloc := range []cpumetrics.CPUAllocation{cpumetrics.CPUAllocationExclusive, cpumetrics.CPUAllocationShared} {
		labels := map[string]string{
			"result":              cpumetrics.ResultError.String(),
			"cpu_allocation_mode": alloc.String(),
		}
		require.Equal(t, float64(0), metricValue(t, reg, "dra_cpu_nri_create_container_duration_seconds", labels))
	}
	require.Equal(t, float64(2), metricValue(t, reg, "dra_cpu_nri_create_container_duration_seconds", map[string]string{
		"result":              cpumetrics.ResultSuccess.String(),
		"cpu_allocation_mode": cpumetrics.CPUAllocationExclusive.String(),
	}))
	require.Equal(t, float64(1), metricValue(t, reg, "dra_cpu_nri_create_container_duration_seconds", map[string]string{
		"result":              cpumetrics.ResultSuccess.String(),
		"cpu_allocation_mode": cpumetrics.CPUAllocationShared.String(),
	}))
}

func TestMetricsNRICreateContainerFailure(t *testing.T) {
	// one of the simplest failure model is intentionally skip to prepare claims
	driver, reg := newMetricsTestDriver(t)

	for _, workload := range minimalWorkloads {
		for _, ctr := range workload.ctrs {
			_, _, err := driver.CreateContainer(context.Background(), workload.pod, ctr)
			if len(ctr.Env) > 0 { // crude proxy for "expects exclusive CPUs"
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		}
	}
	require.Equal(t, float64(0), metricValue(t, reg, "dra_cpu_nri_create_container_duration_seconds", map[string]string{
		"result":              cpumetrics.ResultSuccess.String(),
		"cpu_allocation_mode": cpumetrics.CPUAllocationExclusive.String(),
	}))
	require.Equal(t, float64(2), metricValue(t, reg, "dra_cpu_nri_create_container_duration_seconds", map[string]string{
		"result":              cpumetrics.ResultError.String(),
		"cpu_allocation_mode": cpumetrics.CPUAllocationExclusive.String(),
	}))
	require.Equal(t, float64(1), metricValue(t, reg, "dra_cpu_nri_create_container_duration_seconds", map[string]string{
		"result":              cpumetrics.ResultSuccess.String(),
		"cpu_allocation_mode": cpumetrics.CPUAllocationShared.String(),
	}))
	require.Equal(t, float64(0), metricValue(t, reg, "dra_cpu_nri_create_container_duration_seconds", map[string]string{
		"result":              cpumetrics.ResultError.String(),
		"cpu_allocation_mode": cpumetrics.CPUAllocationShared.String(),
	}))
}

func TestMetricsNRIStopContainer(t *testing.T) {
	// we abuse the fact StopContainer can't fail (nor it should, in the current code shape)
	driver, reg := newMetricsTestDriver(t)

	for _, workload := range minimalWorkloads {
		_, err := driver.PrepareResourceClaims(context.Background(), workload.claims)
		require.NoError(t, err)

		for _, ctr := range workload.ctrs {
			// necessary prep to make sure the accounting is correct
			_, _, err := driver.CreateContainer(context.Background(), workload.pod, ctr)
			require.NoError(t, err)

			_, err = driver.StopContainer(context.Background(), workload.pod, ctr)
			require.NoError(t, err)
		}
	}
	require.Equal(t, float64(2), metricValue(t, reg, "dra_cpu_nri_stop_container_duration_seconds", map[string]string{
		"result":              cpumetrics.ResultSuccess.String(),
		"cpu_allocation_mode": cpumetrics.CPUAllocationExclusive.String(),
	}))
	require.Equal(t, float64(0), metricValue(t, reg, "dra_cpu_nri_stop_container_duration_seconds", map[string]string{
		"result":              cpumetrics.ResultError.String(),
		"cpu_allocation_mode": cpumetrics.CPUAllocationExclusive.String(),
	}))
	require.Equal(t, float64(1), metricValue(t, reg, "dra_cpu_nri_stop_container_duration_seconds", map[string]string{
		"result":              cpumetrics.ResultSuccess.String(),
		"cpu_allocation_mode": cpumetrics.CPUAllocationShared.String(),
	}))
	require.Equal(t, float64(0), metricValue(t, reg, "dra_cpu_nri_stop_container_duration_seconds", map[string]string{
		"result":              cpumetrics.ResultError.String(),
		"cpu_allocation_mode": cpumetrics.CPUAllocationShared.String(),
	}))
}

func TestMetricsNRIRemoveContainer(t *testing.T) {
	// we abuse the fact RemoveContainer can't fail (nor it should, in the current code shape)
	driver, reg := newMetricsTestDriver(t)

	for _, workload := range minimalWorkloads {
		_, err := driver.PrepareResourceClaims(context.Background(), workload.claims)
		require.NoError(t, err)

		for _, ctr := range workload.ctrs {
			// necessary prep to make sure the accounting is correct
			_, _, err := driver.CreateContainer(context.Background(), workload.pod, ctr)
			require.NoError(t, err)

			err = driver.RemoveContainer(context.Background(), workload.pod, ctr)
			require.NoError(t, err)
		}
	}
	require.Equal(t, float64(2), metricValue(t, reg, "dra_cpu_nri_remove_container_duration_seconds", map[string]string{
		"result":              cpumetrics.ResultSuccess.String(),
		"cpu_allocation_mode": cpumetrics.CPUAllocationExclusive.String(),
	}))
	require.Equal(t, float64(0), metricValue(t, reg, "dra_cpu_nri_remove_container_duration_seconds", map[string]string{
		"result":              cpumetrics.ResultError.String(),
		"cpu_allocation_mode": cpumetrics.CPUAllocationExclusive.String(),
	}))
	require.Equal(t, float64(1), metricValue(t, reg, "dra_cpu_nri_remove_container_duration_seconds", map[string]string{
		"result":              cpumetrics.ResultSuccess.String(),
		"cpu_allocation_mode": cpumetrics.CPUAllocationShared.String(),
	}))
	require.Equal(t, float64(0), metricValue(t, reg, "dra_cpu_nri_remove_container_duration_seconds", map[string]string{
		"result":              cpumetrics.ResultError.String(),
		"cpu_allocation_mode": cpumetrics.CPUAllocationShared.String(),
	}))
}
