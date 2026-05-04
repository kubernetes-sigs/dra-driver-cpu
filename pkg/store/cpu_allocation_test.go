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

package store

import (
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/cpuset"
)

func newTestCPUAllocation(allCPUs, reserved cpuset.CPUSet) *CPUAllocation {
	var infos []cpuinfo.CPUInfo
	for _, cpuID := range allCPUs.UnsortedList() {
		infos = append(infos, cpuinfo.CPUInfo{CpuID: cpuID, CoreID: cpuID, SocketID: 0, NUMANodeID: 0})
	}
	mockProvider := &cpuinfo.MockCPUInfoProvider{CPUInfos: infos}
	topo, _ := mockProvider.GetCPUTopology()
	return NewCPUAllocation(topo, reserved)
}

func TestNewCPUAllocation(t *testing.T) {
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
			store := newTestCPUAllocation(tc.allCPUs, tc.reservedCPUs)
			require.NotNil(t, store)
			require.True(t, store.availableCPUs.Equals(tc.expectedAvailable))
			require.True(t, store.GetSharedCPUs().Equals(tc.expectedSharedCPUs))
		})
	}
}

func TestCPUAllocationResourceClaimAllocation(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	store := newTestCPUAllocation(allCPUs, cpuset.New())
	claimUID := types.UID("claim-uid-1")
	cpus := cpuset.New(0, 1)

	// Add allocation
	store.AddResourceClaimAllocation(logger, claimUID, cpus)
	gotCPUs, ok := store.GetResourceClaimAllocation(claimUID)
	require.True(t, ok)
	require.True(t, cpus.Equals(gotCPUs))

	// Remove allocation
	store.RemoveResourceClaimAllocation(logger, claimUID)
	_, ok = store.GetResourceClaimAllocation(claimUID)
	require.False(t, ok)

	// Remove non-existent allocation
	store.RemoveResourceClaimAllocation(logger, types.UID("non-existent"))
}

func TestCPUAllocationGetSharedCPUs(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	reserved := cpuset.New(0)
	store := newTestCPUAllocation(allCPUs, reserved)
	available := allCPUs.Difference(reserved)

	// No allocations
	require.True(t, store.GetSharedCPUs().Equals(available))

	// With allocations
	claimUID1 := types.UID("claim-uid-1")
	cpus1 := cpuset.New(1, 2)
	store.AddResourceClaimAllocation(logger, claimUID1, cpus1)
	expectedShared := available.Difference(cpus1)
	require.True(t, store.GetSharedCPUs().Equals(expectedShared))

	claimUID2 := types.UID("claim-uid-2")
	cpus2 := cpuset.New(3, 4)
	store.AddResourceClaimAllocation(logger, claimUID2, cpus2)
	expectedShared = expectedShared.Difference(cpus2)
	require.True(t, store.GetSharedCPUs().Equals(expectedShared))
}

func TestAddResourceClaimAllocationRepeatedCalls(t *testing.T) {
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	testCases := []struct {
		name           string
		firstCPUs      cpuset.CPUSet
		secondCPUs     cpuset.CPUSet
		expectedClaim  cpuset.CPUSet
		expectedShared cpuset.CPUSet
	}{
		{
			name:           "same cpus repeated",
			firstCPUs:      cpuset.New(0, 1),
			secondCPUs:     cpuset.New(0, 1),
			expectedClaim:  cpuset.New(0, 1),
			expectedShared: cpuset.New(2, 3, 4, 5, 6, 7),
		},
		{
			name:           "different cpus repeated",
			firstCPUs:      cpuset.New(0, 1),
			secondCPUs:     cpuset.New(2, 3),
			expectedClaim:  cpuset.New(2, 3),
			expectedShared: cpuset.New(0, 1, 4, 5, 6, 7),
		},
		{
			name:           "overlapping cpus repeated",
			firstCPUs:      cpuset.New(0, 1, 2),
			secondCPUs:     cpuset.New(1, 2, 3),
			expectedClaim:  cpuset.New(1, 2, 3),
			expectedShared: cpuset.New(0, 4, 5, 6, 7),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			logger := testr.New(t)
			store := newTestCPUAllocation(allCPUs, cpuset.New())
			claimUID := types.UID("claim-uid-1")

			store.AddResourceClaimAllocation(logger, claimUID, tc.firstCPUs)
			store.AddResourceClaimAllocation(logger, claimUID, tc.secondCPUs)

			gotCPUs, ok := store.GetResourceClaimAllocation(claimUID)
			require.True(t, ok)
			require.True(t, tc.expectedClaim.Equals(gotCPUs), "claim cpus mismatch: got %s, want %s", gotCPUs, tc.expectedClaim)
			require.True(t, tc.expectedShared.Equals(store.GetSharedCPUs()), "shared cpus mismatch: got %s, want %s", store.GetSharedCPUs(), tc.expectedShared)
		})
	}
}

func TestCPUAllocationStoreCacheConsistency(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15)
	store := newTestCPUAllocation(allCPUs, cpuset.New())

	store.AddResourceClaimAllocation(logger, "claim-1", cpuset.New(0, 1))
	store.AddResourceClaimAllocation(logger, "claim-2", cpuset.New(2, 3))
	store.AddResourceClaimAllocation(logger, "claim-3", cpuset.New(4, 5))

	expectedShared := cpuset.New(6, 7, 8, 9, 10, 11, 12, 13, 14, 15)
	require.True(t, store.GetSharedCPUs().Equals(expectedShared))

	store.RemoveResourceClaimAllocation(logger, "claim-2")
	expectedShared = cpuset.New(2, 3, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15)
	require.True(t, store.GetSharedCPUs().Equals(expectedShared))

	store.RemoveResourceClaimAllocation(logger, "claim-1")
	expectedShared = cpuset.New(0, 1, 2, 3, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15)
	require.True(t, store.GetSharedCPUs().Equals(expectedShared))

	store.RemoveResourceClaimAllocation(logger, "claim-3")
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
			store := NewCPUAllocation(topo, cpuset.New())
			for claimUID, cpus := range allocations {
				store.AddResourceClaimAllocation(logr.Discard(), claimUID, cpus)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = store.GetSharedCPUs()
			}
		})
	}
}
