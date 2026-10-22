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

func newTestAllocationState(logger logr.Logger, allCPUs, reserved cpuset.CPUSet) *AllocationState {
	var infos []cpuinfo.CPUInfo
	for _, cpuID := range allCPUs.UnsortedList() {
		infos = append(infos, cpuinfo.CPUInfo{CpuID: cpuID, CoreID: cpuID, SocketID: 0, NUMANodeID: 0})
	}
	mockProvider := &cpuinfo.MockCPUInfoProvider{CPUInfos: infos}
	topo, _ := mockProvider.GetCPUTopology(logger)
	return NewAllocationState(topo, reserved)
}

func requirePreparedAllocation(t testing.TB, logger logr.Logger, store *AllocationState, claimUID types.UID, cpus cpuset.CPUSet) {
	t.Helper()
	require.NoError(t, store.ReserveResourceClaimAllocation(logger, claimUID, cpus, false))
}

func TestAllocationStatePreparedLifecycle(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3)
	claimUID := types.UID("claim-1")
	claimCPUs := cpuset.New(0, 1)
	store := newTestAllocationState(logger, allCPUs, cpuset.New())

	require.NoError(t, store.ReserveResourceClaimAllocation(logger, claimUID, claimCPUs, false))
	require.True(t, store.GetSharedCPUs().Equals(cpuset.New(2, 3)))
	require.Error(t, store.ReserveResourceClaimAllocation(logger, "claim-2", claimCPUs, false))

	updatedClaims, err := store.ValidateResourceClaimAllocations(logger, "pod", "app", map[types.UID]cpuset.CPUSet{claimUID: claimCPUs})
	require.NoError(t, err)
	require.Equal(t, []types.UID{claimUID}, updatedClaims)
	require.True(t, store.GetSharedCPUs().Equals(cpuset.New(2, 3)))

	store.RemoveResourceClaimAllocation(logger, claimUID)
	require.False(t, store.IsOwner(claimUID, "pod", "app"))
	require.True(t, store.GetSharedCPUs().Equals(allCPUs))
}

func TestAllocationStateValidateResourceClaimAllocations(t *testing.T) {
	owner := OwnerIdent{PodUID: "pod", ContainerName: "app"}
	allocations := map[types.UID]cpuset.CPUSet{"a": cpuset.New(0), "b": cpuset.New(1)}
	tests := []struct {
		name              string
		initial           *OwnerIdent // Initial owner of claim b.
		expected          map[types.UID]cpuset.CPUSet
		wantUpdatedClaims []types.UID
		wantErr           string
	}{
		{
			name:    "empty",
			wantErr: "no claims to bind",
		},
		{
			name:              "first binding",
			expected:          allocations,
			wantUpdatedClaims: []types.UID{"a", "b"},
		},
		{
			name:     "same owner",
			initial:  &owner,
			expected: map[types.UID]cpuset.CPUSet{"b": cpuset.New(1)},
		},
		{
			name:              "mixed new and existing owners",
			initial:           &owner,
			expected:          allocations,
			wantUpdatedClaims: []types.UID{"a"},
		},
		{
			name:     "pod conflict",
			initial:  &OwnerIdent{PodUID: "other", ContainerName: "app"},
			expected: allocations,
			wantErr:  "already bound",
		},
		{
			name:     "container conflict",
			initial:  &OwnerIdent{PodUID: "pod", ContainerName: "other"},
			expected: allocations,
			wantErr:  "already bound",
		},
		{
			name:     "missing claim",
			expected: map[types.UID]cpuset.CPUSet{"a": cpuset.New(0), "missing": cpuset.New(2)},
			wantErr:  "not prepared",
		},
		{
			name:     "CPU mismatch",
			expected: map[types.UID]cpuset.CPUSet{"a": cpuset.New(0), "b": cpuset.New(2)},
			wantErr:  "cpuset mismatch",
		},
		{
			name:     "CPU mismatch for existing owner",
			initial:  &owner,
			expected: map[types.UID]cpuset.CPUSet{"a": cpuset.New(0), "b": cpuset.New(2)},
			wantErr:  "cpuset mismatch",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logger := testr.New(t)
			store := newTestAllocationState(logger, cpuset.New(0, 1, 2), cpuset.New())
			for uid, cpus := range allocations {
				requirePreparedAllocation(t, logger, store, uid, cpus)
			}
			if tc.initial != nil {
				_, err := store.ValidateResourceClaimAllocations(logger, tc.initial.PodUID, tc.initial.ContainerName, map[types.UID]cpuset.CPUSet{"b": cpuset.New(1)})
				require.NoError(t, err)
			}
			before := store.Clone()
			// bind claim to containers
			updatedClaims, err := store.ValidateResourceClaimAllocations(logger, owner.PodUID, owner.ContainerName, tc.expected)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				require.Equal(t, before.resourceClaimAllocations, store.resourceClaimAllocations, "failed validation must not change any claim")
			} else {
				require.NoError(t, err)
				for uid := range tc.expected {
					require.True(t, store.IsOwner(uid, owner.PodUID, owner.ContainerName))
				}
			}
			require.ElementsMatch(t, tc.wantUpdatedClaims, updatedClaims)
			require.Equal(t, before.Snapshot(), store.Snapshot())
			// Unbind clears only updated claims, preserving prior owners and CPU reservations.
			store.Cleanup(append(updatedClaims, "missing")...)
			require.Equal(t, before.resourceClaimAllocations, store.resourceClaimAllocations)
			require.True(t, store.GetSharedCPUs().Equals(cpuset.New(2)))
		})
	}
}

func TestAllocationStateCloneRetainsOwnership(t *testing.T) {
	logger := testr.New(t)
	store := newTestAllocationState(logger, cpuset.New(0, 1, 2), cpuset.New())
	requirePreparedAllocation(t, logger, store, "a", cpuset.New(0))
	requirePreparedAllocation(t, logger, store, "b", cpuset.New(1))
	_, err := store.ValidateResourceClaimAllocations(logger, "pod", "app", map[types.UID]cpuset.CPUSet{"a": cpuset.New(0)})
	require.NoError(t, err)
	require.True(t, store.IsOwner("a", "pod", "app"))
	require.False(t, store.IsOwner("b", "pod", "app"))

	clone := store.Clone()
	// mirror checks done on the original to make sure they agree
	require.True(t, clone.IsOwner("a", "pod", "app"))
	require.False(t, clone.IsOwner("b", "pod", "app"))
	require.Equal(t, store.Snapshot(), clone.Snapshot())
	// unbind
	clone.Cleanup("a")
	// rebind, now succeeds
	_, err = clone.ValidateResourceClaimAllocations(logger, "other", "app", map[types.UID]cpuset.CPUSet{"a": cpuset.New(0)})
	require.NoError(t, err)
	clone.RemoveResourceClaimAllocation(logger, "b")
	// rebound clone
	require.True(t, clone.IsOwner("a", "other", "app"))
	require.True(t, clone.GetSharedCPUs().Equals(cpuset.New(1, 2)))

	// verify clone mutations didn't affect the original
	require.True(t, store.IsOwner("a", "pod", "app"))
	require.True(t, store.GetSharedCPUs().Equals(cpuset.New(2)))
}

func TestNewAllocationState(t *testing.T) {
	logger := testr.New(t)
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
			store := newTestAllocationState(logger, tc.allCPUs, tc.reservedCPUs)
			require.NotNil(t, store)
			require.True(t, store.availableCPUs.Equals(tc.expectedAvailable))
			require.True(t, store.GetSharedCPUs().Equals(tc.expectedSharedCPUs))
		})
	}
}

func TestAllocationStateResourceClaimAllocation(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	store := newTestAllocationState(logger, allCPUs, cpuset.New())
	claimUID := types.UID("claim-uid-1")
	cpus := cpuset.New(0, 1)

	// Add allocation
	requirePreparedAllocation(t, logger, store, claimUID, cpus)
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

func TestAllocationStateGetSharedCPUs(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	reserved := cpuset.New(0)
	store := newTestAllocationState(logger, allCPUs, reserved)
	available := allCPUs.Difference(reserved)

	// No allocations
	require.True(t, store.GetSharedCPUs().Equals(available))

	// With allocations
	claimUID1 := types.UID("claim-uid-1")
	cpus1 := cpuset.New(1, 2)
	requirePreparedAllocation(t, logger, store, claimUID1, cpus1)
	expectedShared := available.Difference(cpus1)
	require.True(t, store.GetSharedCPUs().Equals(expectedShared))

	claimUID2 := types.UID("claim-uid-2")
	cpus2 := cpuset.New(3, 4)
	requirePreparedAllocation(t, logger, store, claimUID2, cpus2)
	expectedShared = expectedShared.Difference(cpus2)
	require.True(t, store.GetSharedCPUs().Equals(expectedShared))
}

func TestReserveResourceClaimAllocationRepeatedCalls(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	testCases := []struct {
		name        string
		firstCPUs   cpuset.CPUSet
		secondCPUs  cpuset.CPUSet
		expectError bool
	}{
		{
			name:        "same cpus repeated",
			firstCPUs:   cpuset.New(0, 1),
			secondCPUs:  cpuset.New(0, 1),
			expectError: false,
		},
		{
			name:        "different cpus repeated",
			firstCPUs:   cpuset.New(0, 1),
			secondCPUs:  cpuset.New(2, 3),
			expectError: true,
		},
		{
			name:        "overlapping cpus repeated",
			firstCPUs:   cpuset.New(0, 1, 2),
			secondCPUs:  cpuset.New(1, 2, 3),
			expectError: true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestAllocationState(logger, allCPUs, cpuset.New())
			claimUID := types.UID("claim-uid-1")

			require.NoError(t, store.ReserveResourceClaimAllocation(logger, claimUID, tc.firstCPUs, false))
			err := store.ReserveResourceClaimAllocation(logger, claimUID, tc.secondCPUs, false)
			if tc.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			gotCPUs, ok := store.GetResourceClaimAllocation(claimUID)
			require.True(t, ok)
			require.True(t, tc.firstCPUs.Equals(gotCPUs), "claim cpus mismatch: got %s, want %s", gotCPUs, tc.firstCPUs)
			require.True(t, allCPUs.Difference(tc.firstCPUs).Equals(store.GetSharedCPUs()))
		})
	}
}

func TestReserveResourceClaimAllocationSharedPoolGuard(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3)

	tests := []struct {
		name                string
		cpus                cpuset.CPUSet
		hasSharedContainers bool
		wantErr             bool
	}{
		{
			name:                "rejects exhausting pool with shared containers",
			cpus:                allCPUs,
			hasSharedContainers: true,
			wantErr:             true,
		},
		{
			name:                "allows exhausting pool without shared containers",
			cpus:                allCPUs,
			hasSharedContainers: false,
		},
		{
			name:                "allows reservation that leaves shared CPUs",
			cpus:                cpuset.New(0, 1),
			hasSharedContainers: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := newTestAllocationState(logger, allCPUs, cpuset.New())
			err := store.ReserveResourceClaimAllocation(logger, "claim", test.cpus, test.hasSharedContainers)
			if test.wantErr {
				require.ErrorContains(t, err, "would exhaust the shared CPU pool")
				_, ok := store.GetResourceClaimAllocation("claim")
				require.False(t, ok)
				require.True(t, store.GetSharedCPUs().Equals(allCPUs))
			} else {
				require.NoError(t, err)
				require.True(t, store.GetSharedCPUs().Equals(allCPUs.Difference(test.cpus)))
			}
		})
	}
}

func TestAllocationStateStoreCacheConsistency(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15)
	store := newTestAllocationState(logger, allCPUs, cpuset.New())

	requirePreparedAllocation(t, logger, store, "claim-1", cpuset.New(0, 1))
	requirePreparedAllocation(t, logger, store, "claim-2", cpuset.New(2, 3))
	requirePreparedAllocation(t, logger, store, "claim-3", cpuset.New(4, 5))

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

func TestAllocationStateGetReservedCPUs(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	reserved := cpuset.New(0, 1)
	store := newTestAllocationState(logger, allCPUs, reserved)
	require.True(t, store.GetReservedCPUs().Equals(reserved))
}

func TestAllocationStateGetPreparedCPUs(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	store := newTestAllocationState(logger, allCPUs, cpuset.New())

	require.True(t, store.GetPreparedCPUs().IsEmpty())

	claimUID := types.UID("claim-1")
	cpus := cpuset.New(2, 3)
	requirePreparedAllocation(t, logger, store, claimUID, cpus)
	require.True(t, store.GetPreparedCPUs().Equals(cpus))

	store.RemoveResourceClaimAllocation(logger, claimUID)
	require.True(t, store.GetPreparedCPUs().IsEmpty())
}

func TestAllocationStateSnapshot(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	reserved := cpuset.New(0, 1)
	store := newTestAllocationState(logger, allCPUs, reserved)

	require.Equal(t, AllocationSnapshot{
		AllocatedCPUs:        0,
		AvailableCPUs:        6,
		ReservedCPUs:         2,
		ActiveResourceClaims: 0,
	}, store.Snapshot())

	requirePreparedAllocation(t, logger, store, "claim-1", cpuset.New(2, 3))
	require.Equal(t, AllocationSnapshot{
		AllocatedCPUs:        2,
		AvailableCPUs:        4,
		ReservedCPUs:         2,
		ActiveResourceClaims: 1,
	}, store.Snapshot())

	store.RemoveResourceClaimAllocation(logger, "claim-1")
	requirePreparedAllocation(t, logger, store, "claim-1", cpuset.New(4, 5, 6))
	require.Equal(t, AllocationSnapshot{
		AllocatedCPUs:        3,
		AvailableCPUs:        3,
		ReservedCPUs:         2,
		ActiveResourceClaims: 1,
	}, store.Snapshot())

	requirePreparedAllocation(t, logger, store, "claim-2", cpuset.New(2, 3))
	require.Equal(t, AllocationSnapshot{
		AllocatedCPUs:        5,
		AvailableCPUs:        1,
		ReservedCPUs:         2,
		ActiveResourceClaims: 2,
	}, store.Snapshot())

	store.RemoveResourceClaimAllocation(logger, "claim-1")
	require.Equal(t, AllocationSnapshot{
		AllocatedCPUs:        2,
		AvailableCPUs:        4,
		ReservedCPUs:         2,
		ActiveResourceClaims: 1,
	}, store.Snapshot())
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
		topo, _ := mockProvider.GetCPUTopology(logr.Discard())

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
			store := NewAllocationState(topo, cpuset.New())
			for claimUID, cpus := range allocations {
				requirePreparedAllocation(b, logr.Discard(), store, claimUID, cpus)
			}

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = store.GetSharedCPUs()
			}
		})
	}
}
