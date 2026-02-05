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
	"fmt"
	"testing"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/cpuset"
)

func newTestCPUAllocationStore(allCPUs, reserved cpuset.CPUSet) *CPUAllocationStore {
	var infos []cpuinfo.CPUInfo
	for _, cpuID := range allCPUs.UnsortedList() {
		infos = append(infos, cpuinfo.CPUInfo{CpuID: cpuID, CoreID: cpuID, SocketID: 0, NUMANodeID: 0})
	}
	mockProvider := &cpuinfo.MockCPUInfoProvider{CPUInfos: infos}
	topo, _ := mockProvider.GetCPUTopology()
	return NewCPUAllocationStore(topo, reserved)
}

func TestNewCPUAllocationStore(t *testing.T) {
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	testCases := []struct {
		name               string
		allCPUs            cpuset.CPUSet
		reservedCPUs       cpuset.CPUSet
		expectedAvailable  cpuset.CPUSet
		expectedSharedCPUs cpuset.CPUSet
	}{
		{
			name:               "no reserved cpus",
			allCPUs:            allCPUs,
			reservedCPUs:       cpuset.New(),
			expectedAvailable:  allCPUs,
			expectedSharedCPUs: allCPUs,
		},
		{
			name:               "with reserved cpus",
			allCPUs:            allCPUs,
			reservedCPUs:       cpuset.New(0, 1),
			expectedAvailable:  allCPUs.Difference(cpuset.New(0, 1)),
			expectedSharedCPUs: allCPUs.Difference(cpuset.New(0, 1)),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestCPUAllocationStore(tc.allCPUs, tc.reservedCPUs)
			require.NotNil(t, store)
			require.True(t, store.availableCPUs.Equals(tc.expectedAvailable))
			require.True(t, store.GetSharedCPUs().Equals(tc.expectedSharedCPUs))
		})
	}
}

func TestCPUAllocationStoreResourceClaimAllocation(t *testing.T) {
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	store := newTestCPUAllocationStore(allCPUs, cpuset.New())
	claimUID := types.UID("claim-uid-1")
	cpus := cpuset.New(0, 1)

	// Add allocation
	store.AddResourceClaimAllocation(claimUID, cpus)
	gotCPUs, ok := store.GetResourceClaimAllocation(claimUID)
	require.True(t, ok)
	require.True(t, cpus.Equals(gotCPUs))

	// Remove allocation
	store.RemoveResourceClaimAllocation(claimUID)
	_, ok = store.GetResourceClaimAllocation(claimUID)
	require.False(t, ok)

	// Remove non-existent allocation
	store.RemoveResourceClaimAllocation(types.UID("non-existent"))
}

func TestCPUAllocationStoreGetSharedCPUs(t *testing.T) {
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	reserved := cpuset.New(0)
	store := newTestCPUAllocationStore(allCPUs, reserved)
	available := allCPUs.Difference(reserved)

	// No allocations
	require.True(t, store.GetSharedCPUs().Equals(available))

	// With allocations
	claimUID1 := types.UID("claim-uid-1")
	cpus1 := cpuset.New(1, 2)
	store.AddResourceClaimAllocation(claimUID1, cpus1)
	expectedShared := available.Difference(cpus1)
	require.True(t, store.GetSharedCPUs().Equals(expectedShared))

	claimUID2 := types.UID("claim-uid-2")
	cpus2 := cpuset.New(3, 4)
	store.AddResourceClaimAllocation(claimUID2, cpus2)
	expectedShared = expectedShared.Difference(cpus2)
	require.True(t, store.GetSharedCPUs().Equals(expectedShared))
}

func TestCPUAllocationStoreCacheConsistency(t *testing.T) {
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15)
	store := newTestCPUAllocationStore(allCPUs, cpuset.New())

	store.AddResourceClaimAllocation("claim-1", cpuset.New(0, 1))
	store.AddResourceClaimAllocation("claim-2", cpuset.New(2, 3))
	store.AddResourceClaimAllocation("claim-3", cpuset.New(4, 5))

	expectedShared := cpuset.New(6, 7, 8, 9, 10, 11, 12, 13, 14, 15)
	require.True(t, store.GetSharedCPUs().Equals(expectedShared))

	store.RemoveResourceClaimAllocation("claim-2")
	expectedShared = cpuset.New(2, 3, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15)
	require.True(t, store.GetSharedCPUs().Equals(expectedShared))

	store.RemoveResourceClaimAllocation("claim-1")
	expectedShared = cpuset.New(0, 1, 2, 3, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15)
	require.True(t, store.GetSharedCPUs().Equals(expectedShared))

	store.RemoveResourceClaimAllocation("claim-3")
	require.True(t, store.GetSharedCPUs().Equals(allCPUs))
}

func getSharedCPUsNaive(availableCPUs cpuset.CPUSet, allocations map[types.UID]cpuset.CPUSet) cpuset.CPUSet {
	allocated := cpuset.New()
	for _, cpus := range allocations {
		allocated = allocated.Union(cpus)
	}
	return availableCPUs.Difference(allocated)
}

func BenchmarkGetSharedCPUs(b *testing.B) {
	testCases := []struct {
		name           string
		numCPUs        int
		numAllocations int
	}{
		{"10_allocations", 128, 10},
		{"100_allocations", 128, 100},
		{"500_allocations", 1024, 500},
	}

	for _, tc := range testCases {
		var infos []cpuinfo.CPUInfo
		for i := 0; i < tc.numCPUs; i++ {
			infos = append(infos, cpuinfo.CPUInfo{CpuID: i, CoreID: i, SocketID: 0, NUMANodeID: 0})
		}
		mockProvider := &cpuinfo.MockCPUInfoProvider{CPUInfos: infos}
		topo, _ := mockProvider.GetCPUTopology()

		allocations := make(map[types.UID]cpuset.CPUSet)
		for i := 0; i < tc.numAllocations && i*2+1 < tc.numCPUs; i++ {
			allocations[types.UID(fmt.Sprintf("claim-%d", i))] = cpuset.New(i*2, i*2+1)
		}

		cpuIDs := make([]int, 0, tc.numCPUs)
		for i := 0; i < tc.numCPUs; i++ {
			cpuIDs = append(cpuIDs, i)
		}
		availableCPUs := cpuset.New(cpuIDs...)

		b.Run(tc.name+"/naive", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = getSharedCPUsNaive(availableCPUs, allocations)
			}
		})

		b.Run(tc.name+"/optimized", func(b *testing.B) {
			store := NewCPUAllocationStore(topo, cpuset.New())
			for claimUID, cpus := range allocations {
				store.AddResourceClaimAllocation(claimUID, cpus)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = store.GetSharedCPUs()
			}
		})
	}
}
