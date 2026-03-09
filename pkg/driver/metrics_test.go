/*
Copyright 2025 The Kubernetes Authors.

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
	"testing"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	io_prometheus_client "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/utils/cpuset"
)

func TestMetricsCountersOnPrepareUnprepare(t *testing.T) {
	mockProvider := &cpuinfo.MockCPUInfoProvider{CPUInfos: mockCPUInfos_SingleSocket_4CPUS_HT}
	topo, _ := mockProvider.GetCPUTopology()

	newDriver := func() *CPUDriver {
		return &CPUDriver{
			driverName: testDriverName,
			deviceNameToCPUID: map[string]int{
				"cpudev0": 0,
				"cpudev1": 1,
			},
			cpuAllocationStore: store.NewCPUAllocation(topo, cpuset.New()),
			cdiMgr:             newMockCdiMgr(),
		}
	}

	t.Run("prepare success increments counter", func(t *testing.T) {
		driver := newDriver()
		beforeSuccess := testutil.ToFloat64(prepareClaimSuccessTotal)
		beforeError := testutil.ToFloat64(prepareClaimErrorTotal)

		claims := []*resourceapi.ResourceClaim{
			{
				ObjectMeta: metav1.ObjectMeta{UID: "claim-counter-1", Name: "test-claim"},
				Status: resourceapi.ResourceClaimStatus{
					Allocation: &resourceapi.AllocationResult{
						Devices: resourceapi.DeviceAllocationResult{
							Results: []resourceapi.DeviceRequestAllocationResult{
								{Driver: testDriverName, Pool: testNodeName, Device: "cpudev0"},
							},
						},
					},
				},
			},
		}

		_, err := driver.PrepareResourceClaims(context.Background(), claims)
		require.NoError(t, err)

		require.InDelta(t, beforeSuccess+1, testutil.ToFloat64(prepareClaimSuccessTotal), 0.01)
		require.InDelta(t, beforeError, testutil.ToFloat64(prepareClaimErrorTotal), 0.01)
	})

	t.Run("prepare error increments error counter", func(t *testing.T) {
		driver := newDriver()
		beforeError := testutil.ToFloat64(prepareClaimErrorTotal)

		claims := []*resourceapi.ResourceClaim{
			{
				ObjectMeta: metav1.ObjectMeta{UID: "claim-counter-2", Name: "test-claim-err"},
			},
		}

		_, err := driver.PrepareResourceClaims(context.Background(), claims)
		require.NoError(t, err)

		require.InDelta(t, beforeError+1, testutil.ToFloat64(prepareClaimErrorTotal), 0.01)
	})

	t.Run("unprepare increments counter", func(t *testing.T) {
		driver := newDriver()
		before := testutil.ToFloat64(unprepareClaimTotal)

		claims := []kubeletplugin.NamespacedObject{
			{UID: "claim-counter-3"},
		}

		_, err := driver.UnprepareResourceClaims(context.Background(), claims)
		require.NoError(t, err)

		require.InDelta(t, before+1, testutil.ToFloat64(unprepareClaimTotal), 0.01)
	})

	t.Run("prepare records duration histogram", func(t *testing.T) {
		driver := newDriver()

		claims := []*resourceapi.ResourceClaim{
			{
				ObjectMeta: metav1.ObjectMeta{UID: "claim-duration-1", Name: "test-claim-dur"},
				Status: resourceapi.ResourceClaimStatus{
					Allocation: &resourceapi.AllocationResult{
						Devices: resourceapi.DeviceAllocationResult{
							Results: []resourceapi.DeviceRequestAllocationResult{
								{Driver: testDriverName, Pool: testNodeName, Device: "cpudev0"},
							},
						},
					},
				},
			},
		}

		_, err := driver.PrepareResourceClaims(context.Background(), claims)
		require.NoError(t, err)

		require.Greater(t, testutil.CollectAndCount(prepareClaimDuration), 0)
	})

	t.Run("prepare records allocated CPUs histogram", func(t *testing.T) {
		driver := newDriver()

		// Get sample count before
		var m prometheus.Metric
		ch := make(chan prometheus.Metric, 1)
		claimAllocatedCPUs.Collect(ch)
		m = <-ch
		var dto1 io_prometheus_client.Metric
		require.NoError(t, m.Write(&dto1))
		beforeCount := dto1.GetHistogram().GetSampleCount()

		claims := []*resourceapi.ResourceClaim{
			{
				ObjectMeta: metav1.ObjectMeta{UID: "claim-alloc-cpus-1", Name: "test-claim-alloc"},
				Status: resourceapi.ResourceClaimStatus{
					Allocation: &resourceapi.AllocationResult{
						Devices: resourceapi.DeviceAllocationResult{
							Results: []resourceapi.DeviceRequestAllocationResult{
								{Driver: testDriverName, Pool: testNodeName, Device: "cpudev0"},
								{Driver: testDriverName, Pool: testNodeName, Device: "cpudev1"},
							},
						},
					},
				},
			},
		}

		_, err := driver.PrepareResourceClaims(context.Background(), claims)
		require.NoError(t, err)

		// Get sample count after
		ch2 := make(chan prometheus.Metric, 1)
		claimAllocatedCPUs.Collect(ch2)
		m = <-ch2
		var dto2 io_prometheus_client.Metric
		require.NoError(t, m.Write(&dto2))
		afterCount := dto2.GetHistogram().GetSampleCount()

		require.Greater(t, afterCount, beforeCount)
	})
}
