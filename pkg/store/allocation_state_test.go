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
	"maps"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/checkpoint"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/cpuset"
)

var noop = checkpoint.NewNOOPManager()

func newTestAllocationState(t *testing.T, logger logr.Logger, allCPUs, reserved cpuset.CPUSet) *AllocationState {
	t.Helper()
	var infos []cpuinfo.CPUInfo
	for _, cpuID := range allCPUs.UnsortedList() {
		infos = append(infos, cpuinfo.CPUInfo{CpuID: cpuID, CoreID: cpuID, SocketID: 0, NUMANodeID: 0})
	}
	mockProvider := &cpuinfo.MockCPUInfoProvider{CPUInfos: infos}
	topo, err := mockProvider.GetCPUTopology(logger)
	require.NoError(t, err) // if fail there's a so severe bug that we should be loud
	return NewAllocationState(topo, reserved)
}

func requirePreparedAllocation(t testing.TB, logger logr.Logger, store *AllocationState, claimUID types.UID, cpus cpuset.CPUSet) {
	t.Helper()
	require.NoError(t, store.Reserve(logger, noop, claimUID, map[string]cpuset.CPUSet{"": cpus}, false))
}

func TestAllocationStatePreparedLifecycle(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3)
	claimUID := types.UID("claim-1")
	claimCPUs := cpuset.New(0, 1)
	store := newTestAllocationState(t, logger, allCPUs, cpuset.New())

	require.NoError(t, store.Reserve(logger, noop, claimUID, map[string]cpuset.CPUSet{"": claimCPUs}, false))
	require.True(t, store.GetSharedCPUs().Equals(cpuset.New(2, 3)))
	require.Error(t, store.Reserve(logger, noop, "claim-2", map[string]cpuset.CPUSet{"": claimCPUs}, false))

	updatedClaims, err := store.Bind(logger, noop, "pod", "app", map[types.UID]cpuset.CPUSet{claimUID: claimCPUs})
	require.NoError(t, err)
	require.Equal(t, []types.UID{claimUID}, updatedClaims)
	require.True(t, store.GetSharedCPUs().Equals(cpuset.New(2, 3)))

	require.NoError(t, store.Release(logger, noop, claimUID))
	require.False(t, store.IsOwner(claimUID, "pod", "app"))
	require.True(t, store.GetSharedCPUs().Equals(allCPUs))
}

func TestAllocationStateBind(t *testing.T) {
	owner := OwnerIdent{
		PodUID:        "pod",
		ContainerName: "app",
	}
	allocations := map[types.UID]cpuset.CPUSet{
		"a": cpuset.New(0),
		"b": cpuset.New(1),
	}
	tests := []struct {
		name              string
		initial           *OwnerIdent // Initial owner of claim b.
		expected          map[types.UID]cpuset.CPUSet
		wantUpdatedClaims []types.UID
		wantClaimInfo     map[string]claimAllocationCheckpoint
		wantErrIs         error
	}{
		{
			name:      "empty claim list",
			wantErrIs: ErrEmptyClaimList,
		},
		{
			name:              "first binding",
			expected:          allocations,
			wantUpdatedClaims: []types.UID{"a", "b"},
			wantClaimInfo: map[string]claimAllocationCheckpoint{
				"a": {
					CPUs: "0",
					Requests: map[string]requestAllocationCheckpoint{
						"": {
							CPUs: "0",
							Owners: []ownerIdentCheckpoint{
								{PodUID: "pod", ContainerName: "app"},
							},
						},
					},
				},
				"b": {
					CPUs: "1",
					Requests: map[string]requestAllocationCheckpoint{
						"": {
							CPUs: "1",
							Owners: []ownerIdentCheckpoint{
								{PodUID: "pod", ContainerName: "app"},
							},
						},
					},
				},
				"c": {
					CPUs: "2-3",
					Requests: map[string]requestAllocationCheckpoint{
						"x": {
							CPUs:   "2",
							Owners: []ownerIdentCheckpoint{},
						},
						"y": {
							CPUs:   "3",
							Owners: []ownerIdentCheckpoint{},
						},
					},
				},
			},
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
			wantClaimInfo: map[string]claimAllocationCheckpoint{
				"a": {
					CPUs: "0",
					Requests: map[string]requestAllocationCheckpoint{
						"": {
							CPUs: "0",
							Owners: []ownerIdentCheckpoint{
								{PodUID: "pod", ContainerName: "app"},
							},
						},
					},
				},
				"b": {
					CPUs: "1",
					Requests: map[string]requestAllocationCheckpoint{
						"": {
							CPUs: "1",
							Owners: []ownerIdentCheckpoint{
								{PodUID: "pod", ContainerName: "app"},
							},
						},
					},
				},
				"c": {
					CPUs: "2-3",
					Requests: map[string]requestAllocationCheckpoint{
						"x": {
							CPUs:   "2",
							Owners: []ownerIdentCheckpoint{},
						},
						"y": {
							CPUs:   "3",
							Owners: []ownerIdentCheckpoint{},
						},
					},
				},
			},
		},
		{
			name:              "multi-request claim",
			expected:          map[types.UID]cpuset.CPUSet{"c": cpuset.New(2, 3)},
			wantUpdatedClaims: []types.UID{"c"},
			wantClaimInfo: map[string]claimAllocationCheckpoint{
				"a": {
					CPUs: "0",
					Requests: map[string]requestAllocationCheckpoint{
						"": {
							CPUs:   "0",
							Owners: []ownerIdentCheckpoint{},
						},
					},
				},
				"b": {
					CPUs: "1",
					Requests: map[string]requestAllocationCheckpoint{
						"": {
							CPUs:   "1",
							Owners: []ownerIdentCheckpoint{},
						},
					},
				},
				"c": {
					CPUs: "2-3",
					Requests: map[string]requestAllocationCheckpoint{
						"x": {
							CPUs: "2",
							Owners: []ownerIdentCheckpoint{
								{PodUID: "pod", ContainerName: "app"},
							},
						},
						"y": {
							CPUs: "3",
							Owners: []ownerIdentCheckpoint{
								{PodUID: "pod", ContainerName: "app"},
							},
						},
					},
				},
			},
		},
		{
			name:      "pod conflict",
			initial:   &OwnerIdent{PodUID: "other", ContainerName: "app"},
			expected:  allocations,
			wantErrIs: ErrClaimAlreadyBound,
		},
		{
			name:      "container conflict",
			initial:   &OwnerIdent{PodUID: "pod", ContainerName: "other"},
			expected:  allocations,
			wantErrIs: ErrClaimAlreadyBound,
		},
		{
			name:      "missing claim",
			expected:  map[types.UID]cpuset.CPUSet{"a": cpuset.New(0), "missing": cpuset.New(2)},
			wantErrIs: ErrUnknownClaim,
		},
		{
			name:      "CPU mismatch",
			expected:  map[types.UID]cpuset.CPUSet{"a": cpuset.New(0), "b": cpuset.New(2)},
			wantErrIs: ErrClaimCPUMismatch,
		},
		{
			name:      "CPU mismatch for existing owner",
			initial:   &owner,
			expected:  map[types.UID]cpuset.CPUSet{"a": cpuset.New(0), "b": cpuset.New(2)},
			wantErrIs: ErrClaimCPUMismatch,
		},
		{
			// The CDI env has one cpuset per claim, so one request is a mismatch.
			name:      "one request of a multi-request claim",
			expected:  map[types.UID]cpuset.CPUSet{"c": cpuset.New(2)},
			wantErrIs: ErrClaimCPUMismatch,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			logger := testr.New(t)
			manager := newRecordingCheckpointManager(t)
			store := newTestAllocationState(t, logger, cpuset.New(0, 1, 2, 3, 4, 5), cpuset.New())

			// add a sentinel which pre-exists and is not mutated by the binding under tests, so we check no unrelated overwrites
			sentinelOwner := OwnerIdent{
				PodUID:        "other-pod",
				ContainerName: "other-app",
			}
			require.NoError(t, store.Reserve(logger, manager, "sentinel", map[string]cpuset.CPUSet{"": cpuset.New(4)}, false))
			_, err := store.Bind(logger, manager, sentinelOwner.PodUID, sentinelOwner.ContainerName, map[types.UID]cpuset.CPUSet{"sentinel": cpuset.New(4)})
			require.NoError(t, err)

			wantClaimInfo := map[string]claimAllocationCheckpoint{
				"sentinel": {
					CPUs: "4",
					Requests: map[string]requestAllocationCheckpoint{
						"": {
							CPUs: "4",
							Owners: []ownerIdentCheckpoint{
								{PodUID: "other-pod", ContainerName: "other-app"},
							},
						},
					},
				},
			}

			for uid, cpus := range allocations {
				require.NoError(t, store.Reserve(logger, manager, uid, map[string]cpuset.CPUSet{"": cpus}, false))
			}
			require.NoError(t, store.Reserve(logger, manager, "c", map[string]cpuset.CPUSet{"x": cpuset.New(2), "y": cpuset.New(3)}, false))
			if tc.initial != nil {
				_, err := store.Bind(logger, manager, tc.initial.PodUID, tc.initial.ContainerName, map[types.UID]cpuset.CPUSet{"b": cpuset.New(1)})
				require.NoError(t, err)
			}
			allocStateBefore := store.Clone()
			checkpointBefore := readAllocationCheckpoint(t, manager)
			createAttemptsBefore := manager.createAttempts

			updatedClaims, err := store.Bind(logger, manager, owner.PodUID, owner.ContainerName, tc.expected)
			if tc.wantErrIs != nil {
				require.ErrorIs(t, err, tc.wantErrIs)
				require.Equal(t, allocStateBefore.resourceClaimAllocations, store.resourceClaimAllocations, "failed validation must not change any claim")
			} else {
				require.NoError(t, err)
				for uid := range tc.expected {
					require.True(t, store.IsOwner(uid, owner.PodUID, owner.ContainerName))
				}
			}
			require.ElementsMatch(t, tc.wantUpdatedClaims, updatedClaims)
			if len(tc.wantUpdatedClaims) == 0 {
				require.Equal(t, createAttemptsBefore, manager.createAttempts)
				require.Equal(t, checkpointBefore, readAllocationCheckpoint(t, manager))
			} else {
				require.Equal(t, createAttemptsBefore+1, manager.createAttempts)
				maps.Insert(wantClaimInfo, maps.All(tc.wantClaimInfo))
				require.Equal(t, wantClaimInfo, readAllocationCheckpoint(t, manager).Payload.ClaimInfo)
			}
			require.Equal(t, allocStateBefore.Snapshot(), store.Snapshot())
			// Unbind clears only updated claims, preserving prior owners and CPU reservations.
			require.NoError(t, store.Unbind(logger, noop, append(updatedClaims, "missing")...))
			require.Equal(t, allocStateBefore.resourceClaimAllocations, store.resourceClaimAllocations)
			require.True(t, store.GetSharedCPUs().Equals(cpuset.New(5)))
		})
	}
}

func TestAllocationStateBindErrorDetails(t *testing.T) {
	logger := testr.New(t)
	store := newTestAllocationState(t, logger, cpuset.New(0, 1, 2), cpuset.New())
	requirePreparedAllocation(t, logger, store, "a", cpuset.New(0))

	_, err := store.Bind(logger, noop, "pod", "app", map[types.UID]cpuset.CPUSet{"missing": cpuset.New(2)})
	var unknownClaimErr UnknownClaimError
	require.ErrorAs(t, err, &unknownClaimErr)
	require.Equal(t, types.UID("missing"), unknownClaimErr.ClaimUID)

	_, err = store.Bind(logger, noop, "pod", "app", map[types.UID]cpuset.CPUSet{"a": cpuset.New(2)})
	var mismatchErr ClaimCPUMismatchError
	require.ErrorAs(t, err, &mismatchErr)
	require.Equal(t, types.UID("a"), mismatchErr.ClaimUID)
	require.True(t, mismatchErr.Expected.Equals(cpuset.New(0)))
	require.True(t, mismatchErr.Got.Equals(cpuset.New(2)))

	_, err = store.Bind(logger, noop, "", "app", map[types.UID]cpuset.CPUSet{"a": cpuset.New(0)})
	require.ErrorContains(t, err, "invalid claim owner")
	require.False(t, store.IsOwner("a", "", "app"))

	_, err = store.Bind(logger, noop, "pod", "", map[types.UID]cpuset.CPUSet{"a": cpuset.New(0)})
	require.ErrorContains(t, err, "invalid claim owner")
	require.False(t, store.IsOwner("a", "pod", ""))
}

func TestAllocationStateCloneRetainsOwnership(t *testing.T) {
	logger := testr.New(t)
	store := newTestAllocationState(t, logger, cpuset.New(0, 1, 2), cpuset.New())
	requirePreparedAllocation(t, logger, store, "a", cpuset.New(0))
	requirePreparedAllocation(t, logger, store, "b", cpuset.New(1))
	_, err := store.Bind(logger, noop, "pod", "app", map[types.UID]cpuset.CPUSet{"a": cpuset.New(0)})
	require.NoError(t, err)
	require.True(t, store.IsOwner("a", "pod", "app"))
	require.False(t, store.IsOwner("b", "pod", "app"))

	clone := store.Clone()
	// mirror checks done on the original to make sure they agree
	require.True(t, clone.IsOwner("a", "pod", "app"))
	require.False(t, clone.IsOwner("b", "pod", "app"))
	require.Equal(t, store.Snapshot(), clone.Snapshot())
	require.NoError(t, clone.Unbind(logger, noop, "a"))
	_, err = clone.Bind(logger, noop, "other", "app", map[types.UID]cpuset.CPUSet{"a": cpuset.New(0)})
	require.NoError(t, err)
	require.NoError(t, clone.Release(logger, noop, "b"))
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
			store := newTestAllocationState(t, logger, tc.allCPUs, tc.reservedCPUs)
			require.NotNil(t, store)
			require.True(t, store.availableCPUs.Equals(tc.expectedAvailable))
			require.True(t, store.GetSharedCPUs().Equals(tc.expectedSharedCPUs))
		})
	}
}

func TestAllocationStateResourceClaimAllocation(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	store := newTestAllocationState(t, logger, allCPUs, cpuset.New())
	claimUID := types.UID("claim-uid-1")
	cpus := cpuset.New(0, 1)

	// Add allocation
	requirePreparedAllocation(t, logger, store, claimUID, cpus)
	gotCPUs, ok := store.GetResourceClaimAllocation(claimUID)
	require.True(t, ok)
	require.True(t, cpus.Equals(gotCPUs))

	// Remove allocation
	require.NoError(t, store.Release(logger, noop, claimUID))
	_, ok = store.GetResourceClaimAllocation(claimUID)
	require.False(t, ok)

	// Remove non-existent allocation
	require.NoError(t, store.Release(logger, noop, types.UID("non-existent")))
}

func TestAllocationStateGetSharedCPUs(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	reserved := cpuset.New(0)
	store := newTestAllocationState(t, logger, allCPUs, reserved)
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

func TestAllocationStateReserveRepeatedCalls(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	// IMPORTANT NOTE: all the claims in the testcases either
	// - cause no updates (re-reserve identical claim)
	// - cause a validation error, which in turn prevent a re-reserve write
	// therefore we can safely test a repeated Reserve call (firstCPUs then
	// secondCPUs) causes no storage write. Keep the testcases preserving
	// this invariant, or split the test accordingly when extending.
	testCases := []struct {
		name          string
		firstRequests map[string]cpuset.CPUSet // per-request split of firstCPUs, if set
		firstCPUs     cpuset.CPUSet
		secondCPUs    cpuset.CPUSet
		expectError   bool
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
		{
			name:          "per-request cpus repeated as the entire claim",
			firstRequests: map[string]cpuset.CPUSet{"a": cpuset.New(0, 1), "b": cpuset.New(2, 3)},
			firstCPUs:     cpuset.New(0, 1, 2, 3),
			secondCPUs:    cpuset.New(0, 1, 2, 3),
			expectError:   false,
		},
		{
			name:          "per-request cpus repeated with other cpus",
			firstRequests: map[string]cpuset.CPUSet{"a": cpuset.New(0, 1), "b": cpuset.New(2, 3)},
			firstCPUs:     cpuset.New(0, 1, 2, 3),
			secondCPUs:    cpuset.New(0, 1),
			expectError:   true,
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			store := newTestAllocationState(t, logger, allCPUs, cpuset.New())
			claimUID := types.UID("claim-uid-1")
			manager := newRecordingCheckpointManager(t)

			first := map[string]cpuset.CPUSet{"": tc.firstCPUs}
			if tc.firstRequests != nil {
				first = tc.firstRequests
			}
			require.NoError(t, store.Reserve(logger, manager, claimUID, first, false))

			// overloading the test to check the write invariant
			before := readAllocationCheckpoint(t, manager)
			writes := manager.createAttempts

			err := store.Reserve(logger, manager, claimUID, map[string]cpuset.CPUSet{"": tc.secondCPUs}, false)
			if tc.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}

			require.Equal(t, writes, manager.createAttempts)
			require.Equal(t, before, readAllocationCheckpoint(t, manager))
			gotCPUs, ok := store.GetResourceClaimAllocation(claimUID)
			require.True(t, ok)
			require.True(t, tc.firstCPUs.Equals(gotCPUs), "claim cpus mismatch: got %s, want %s", gotCPUs, tc.firstCPUs)
			require.True(t, allCPUs.Difference(tc.firstCPUs).Equals(store.GetSharedCPUs()))
		})
	}
}

func TestAllocationStateReserveSharedPoolGuard(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	claimedCPUs := cpuset.New(0, 1, 2, 3)

	tests := []struct {
		name                string
		cpus                cpuset.CPUSet
		requests            map[string]cpuset.CPUSet // replaces cpus, if set
		hasSharedContainers bool
		wantErr             string
	}{
		{
			name:                "rejects exhausting pool with shared containers",
			cpus:                claimedCPUs,
			hasSharedContainers: true,
			wantErr:             "would exhaust the shared CPU pool",
		},
		{
			name:                "allows exhausting pool without shared containers",
			cpus:                claimedCPUs,
			hasSharedContainers: false,
		},
		{
			name:                "allows reservation that leaves shared CPUs",
			cpus:                cpuset.New(0, 1),
			hasSharedContainers: true,
		},
		{
			name:     "rejects overlapping requests",
			requests: map[string]cpuset.CPUSet{"a": cpuset.New(0, 1), "b": cpuset.New(1, 2)},
			wantErr:  "overlap another request",
		},
		{
			name:     "rejects a request without CPUs",
			requests: map[string]cpuset.CPUSet{"a": cpuset.New(0, 1), "b": cpuset.New()},
			wantErr:  `request "b" has no CPUs`,
		},
		{
			name:     "rejects a claim without requests",
			requests: map[string]cpuset.CPUSet{},
			wantErr:  "has no CPUs",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := newRecordingCheckpointManager(t)
			store := newTestAllocationState(t, logger, allCPUs, cpuset.New())

			// add a sentinel which pre-exists and is not mutated by the reserve under tests, so we check no unrelated overwrites
			require.NoError(t, store.Reserve(logger, manager, "sentinel", map[string]cpuset.CPUSet{"": allCPUs.Difference(claimedCPUs)}, false))
			stateBefore := readAllocationCheckpoint(t, manager)
			writesBefore := manager.createAttempts

			// the real reserve under test
			requests := test.requests
			if requests == nil {
				requests = map[string]cpuset.CPUSet{"": test.cpus}
			}
			err := store.Reserve(logger, manager, "claim", requests, test.hasSharedContainers)

			// TODO: verification gets very complex very fast. We should probably split the tests in happy and error path to simplify
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				_, ok := store.GetResourceClaimAllocation("claim")
				require.False(t, ok)
				require.True(t, store.GetSharedCPUs().Equals(claimedCPUs))
				require.Equal(t, writesBefore, manager.createAttempts)
				require.Equal(t, stateBefore, readAllocationCheckpoint(t, manager))
			} else {
				require.NoError(t, err)
				require.True(t, store.GetSharedCPUs().Equals(claimedCPUs.Difference(test.cpus)))
				require.Equal(t, writesBefore+1, manager.createAttempts)
				want := map[string]claimAllocationCheckpoint{
					"sentinel": {
						CPUs: "4-7",
						Requests: map[string]requestAllocationCheckpoint{
							"": {
								CPUs:   "4-7",
								Owners: []ownerIdentCheckpoint{},
							},
						},
					},
					"claim": {
						CPUs: test.cpus.String(),
						Requests: map[string]requestAllocationCheckpoint{
							"": {
								CPUs:   test.cpus.String(),
								Owners: []ownerIdentCheckpoint{},
							},
						},
					},
				}
				require.Equal(t, want, readAllocationCheckpoint(t, manager).Payload.ClaimInfo)
			}
		})
	}
}

func TestAllocationStateStoreCacheConsistency(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15)
	store := newTestAllocationState(t, logger, allCPUs, cpuset.New())

	requirePreparedAllocation(t, logger, store, "claim-1", cpuset.New(0, 1))
	requirePreparedAllocation(t, logger, store, "claim-2", cpuset.New(2, 3))
	requirePreparedAllocation(t, logger, store, "claim-3", cpuset.New(4, 5))

	expectedShared := cpuset.New(6, 7, 8, 9, 10, 11, 12, 13, 14, 15)
	require.True(t, store.GetSharedCPUs().Equals(expectedShared))

	require.NoError(t, store.Release(logger, noop, "claim-2"))
	expectedShared = cpuset.New(2, 3, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15)
	require.True(t, store.GetSharedCPUs().Equals(expectedShared))

	require.NoError(t, store.Release(logger, noop, "claim-1"))
	expectedShared = cpuset.New(0, 1, 2, 3, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15)
	require.True(t, store.GetSharedCPUs().Equals(expectedShared))

	require.NoError(t, store.Release(logger, noop, "claim-3"))
	require.True(t, store.GetSharedCPUs().Equals(allCPUs))
}

func TestAllocationStateGetReservedCPUs(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	reserved := cpuset.New(0, 1)
	store := newTestAllocationState(t, logger, allCPUs, reserved)
	require.True(t, store.GetReservedCPUs().Equals(reserved))
}

func TestAllocationStateGetPreparedCPUs(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	store := newTestAllocationState(t, logger, allCPUs, cpuset.New())

	require.True(t, store.GetPreparedCPUs().IsEmpty())

	claimUID := types.UID("claim-1")
	cpus := cpuset.New(2, 3)
	requirePreparedAllocation(t, logger, store, claimUID, cpus)
	require.True(t, store.GetPreparedCPUs().Equals(cpus))

	require.NoError(t, store.Release(logger, noop, claimUID))
	require.True(t, store.GetPreparedCPUs().IsEmpty())
}

func TestAllocationStateSnapshot(t *testing.T) {
	logger := testr.New(t)
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	reserved := cpuset.New(0, 1)
	store := newTestAllocationState(t, logger, allCPUs, reserved)

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

	require.NoError(t, store.Release(logger, noop, "claim-1"))
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

	require.NoError(t, store.Release(logger, noop, "claim-1"))
	require.Equal(t, AllocationSnapshot{
		AllocatedCPUs:        2,
		AvailableCPUs:        4,
		ReservedCPUs:         2,
		ActiveResourceClaims: 1,
	}, store.Snapshot())
}
