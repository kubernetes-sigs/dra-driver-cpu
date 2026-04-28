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
	"testing"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/metrics"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	"github.com/prometheus/client_golang/prometheus/testutil"
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
		metrics.ResetForTest()
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

		success := metrics.Default.PrepareClaimTotal.WithLabelValues("success")
		errCtr := metrics.Default.PrepareClaimTotal.WithLabelValues("error")
		require.InDelta(t, 1, testutil.ToFloat64(success), 0.01)
		require.InDelta(t, 0, testutil.ToFloat64(errCtr), 0.01)
	})

	t.Run("prepare error increments error counter", func(t *testing.T) {
		driver := newDriver()
		claims := []*resourceapi.ResourceClaim{
			{ObjectMeta: metav1.ObjectMeta{UID: "claim-counter-2", Name: "test-claim-err"}},
		}
		_, err := driver.PrepareResourceClaims(context.Background(), claims)
		require.NoError(t, err)

		errCtr := metrics.Default.PrepareClaimTotal.WithLabelValues("error")
		require.InDelta(t, 1, testutil.ToFloat64(errCtr), 0.01)
	})

	t.Run("unprepare increments counter", func(t *testing.T) {
		driver := newDriver()
		claims := []kubeletplugin.NamespacedObject{{UID: "claim-counter-3"}}
		_, err := driver.UnprepareResourceClaims(context.Background(), claims)
		require.NoError(t, err)
		require.InDelta(t, 1, testutil.ToFloat64(metrics.Default.UnprepareClaimTotal), 0.01)
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
		require.Greater(t, testutil.CollectAndCount(metrics.Default.PrepareClaimDuration), 0)
	})

	t.Run("prepare records allocated CPUs histogram", func(t *testing.T) {
		driver := newDriver()
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
		require.Equal(t, 1, testutil.CollectAndCount(metrics.Default.ClaimAllocatedCPUs))
	})
}
