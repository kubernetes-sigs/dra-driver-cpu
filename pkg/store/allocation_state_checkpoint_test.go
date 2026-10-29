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

package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/checkpoint"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/cpuset"
)

type failingCheckpointManager struct {
	err error
}

func (m failingCheckpointManager) CreateCheckpoint(string, checkpoint.Checkpoint) error {
	return m.err
}

func (m failingCheckpointManager) GetCheckpoint(string, checkpoint.Checkpoint) error {
	return m.err
}

func (m failingCheckpointManager) RemoveCheckpoint(string) error {
	return m.err
}

func (m failingCheckpointManager) ListCheckpoints() ([]string, error) {
	return nil, m.err
}

func (m failingCheckpointManager) CheckpointPath() string {
	return ""
}

type recordingCheckpointManager struct {
	checkpoint.Manager
	createAttempts int
	failingMgr     checkpoint.Manager
}

func newRecordingCheckpointManager(t *testing.T) *recordingCheckpointManager {
	t.Helper()
	manager, err := checkpoint.NewManager(t.TempDir())
	require.NoError(t, err)
	return &recordingCheckpointManager{Manager: manager}
}

func (m *recordingCheckpointManager) CreateCheckpoint(name string, cp checkpoint.Checkpoint) error {
	m.createAttempts++
	if m.failingMgr != nil {
		return m.failingMgr.CreateCheckpoint(name, cp)
	}
	return m.Manager.CreateCheckpoint(name, cp)
}

func readAllocationCheckpoint(t *testing.T, manager checkpoint.Manager) allocationStateCheckpointData {
	t.Helper()
	cp := &AllocationStateCheckpoint{}
	require.NoError(t, manager.GetCheckpoint(allocationCheckpointName, cp))
	return cp.checkpointData
}

func TestAllocationStateReserveKeepsStateWhenCheckpointWriteFails(t *testing.T) {
	logger := testr.New(t)
	storage := newRecordingCheckpointManager(t)
	allCPUs := cpuset.New(0, 1, 2, 3)
	store := newTestAllocationState(t, logger, allCPUs, cpuset.New())

	// add a sentinel which pre-exists and is not mutated by the binding under tests, so we check no unrelated overwrites
	require.NoError(t, store.Reserve(logger, storage, ClaimIdent{UID: "sentinel"}, cpuset.New(3), false))
	checkpointBefore := readAllocationCheckpoint(t, storage)
	createAttemptsBefore := storage.createAttempts
	allocStateBefore := store.Clone()

	manager := failingCheckpointManager{
		err: errors.New("checkpoint write failed"),
	}

	storage.failingMgr = manager
	err := store.Reserve(logger, storage, ClaimIdent{UID: "claim"}, cpuset.New(0), false)
	require.ErrorContains(t, err, "checkpoint write failed")
	require.ErrorIs(t, err, manager.err)

	require.Equal(t, createAttemptsBefore+1, storage.createAttempts)
	require.Equal(t, checkpointBefore, readAllocationCheckpoint(t, storage))
	require.Equal(t, allocStateBefore.Snapshot(), store.Snapshot())
	_, ok := store.GetResourceClaimAllocation("claim")
	require.False(t, ok)
}

func TestAllocationStateBindKeepsStateWhenCheckpointWriteFails(t *testing.T) {
	logger := testr.New(t)
	storage := newRecordingCheckpointManager(t)
	allCPUs := cpuset.New(0, 1, 2, 3)
	store := newTestAllocationState(t, logger, allCPUs, cpuset.New())

	require.NoError(t, store.Reserve(logger, storage, ClaimIdent{UID: "claim"}, cpuset.New(0), false))
	require.NoError(t, store.Reserve(logger, storage, ClaimIdent{UID: "second"}, cpuset.New(1), false))

	// add a sentinel which pre-exists and is not mutated by the binding under tests, so we check no unrelated overwrites
	require.NoError(t, store.Reserve(logger, storage, ClaimIdent{UID: "sentinel"}, cpuset.New(3), false))
	_, err := store.Bind(logger, storage, "other-pod", "other-app", map[types.UID]cpuset.CPUSet{"sentinel": cpuset.New(3)})
	require.NoError(t, err)
	checkpointBefore := readAllocationCheckpoint(t, storage)
	createAttemptsBefore := storage.createAttempts
	allocStateBefore := store.Clone()

	manager := failingCheckpointManager{
		err: errors.New("checkpoint write failed"),
	}

	storage.failingMgr = manager
	// prefer to exercise multiple claims in a single bind, as it's the most complex scenario
	// because it could expose partial binding claims. Claims should always be bound in all-or-nothing fashion.
	updated, err := store.Bind(logger, storage, "pod", "container", map[types.UID]cpuset.CPUSet{
		"claim":  cpuset.New(0),
		"second": cpuset.New(1),
	})
	require.Empty(t, updated)
	require.ErrorContains(t, err, "checkpoint write failed")
	require.ErrorIs(t, err, manager.err)

	require.Equal(t, createAttemptsBefore+1, storage.createAttempts)
	require.Equal(t, checkpointBefore, readAllocationCheckpoint(t, storage))
	require.Equal(t, allocStateBefore.resourceClaimAllocations, store.resourceClaimAllocations)
	require.False(t, store.IsOwner("claim", "pod", "container"))
	require.False(t, store.IsOwner("second", "pod", "container"))
	require.True(t, store.IsOwner("sentinel", "other-pod", "other-app"))
}

func TestAllocationStateUnbindKeepsStateWhenCheckpointWriteFails(t *testing.T) {
	logger := testr.New(t)
	storage := newRecordingCheckpointManager(t)
	allCPUs := cpuset.New(0, 1, 2, 3)
	store := newTestAllocationState(t, logger, allCPUs, cpuset.New())

	require.NoError(t, store.Reserve(logger, storage, ClaimIdent{UID: "claim"}, cpuset.New(0), false))
	_, err := store.Bind(logger, storage, "pod", "container", map[types.UID]cpuset.CPUSet{"claim": cpuset.New(0)})
	require.NoError(t, err)
	checkpointBefore := readAllocationCheckpoint(t, storage)
	createAttemptsBefore := storage.createAttempts
	allocStateBefore := store.Clone()

	manager := failingCheckpointManager{
		err: errors.New("checkpoint write failed"),
	}

	storage.failingMgr = manager
	err = store.Unbind(logger, storage, "claim")
	require.ErrorContains(t, err, "checkpoint write failed")
	require.ErrorIs(t, err, manager.err)

	require.Equal(t, createAttemptsBefore+1, storage.createAttempts)
	require.Equal(t, checkpointBefore, readAllocationCheckpoint(t, storage))
	require.Equal(t, allocStateBefore.resourceClaimAllocations, store.resourceClaimAllocations)
	require.True(t, store.IsOwner("claim", "pod", "container"))
}

func TestAllocationStateUnbindBatch(t *testing.T) {
	logger := testr.New(t)
	storage := newRecordingCheckpointManager(t)
	state := newTestAllocationState(t, logger, cpuset.New(0, 1, 2, 3), cpuset.New())
	allocations := map[types.UID]cpuset.CPUSet{
		"claim":    cpuset.New(0),
		"claim2":   cpuset.New(1),
		"vacant":   cpuset.New(2),
		"sentinel": cpuset.New(3),
	}
	for uid, cpus := range allocations {
		require.NoError(t, state.Reserve(logger, storage, ClaimIdent{UID: uid}, cpus, false))
	}
	_, err := state.Bind(logger, storage, "pod", "container", map[types.UID]cpuset.CPUSet{
		"claim":    cpuset.New(0),
		"claim2":   cpuset.New(1),
		"sentinel": cpuset.New(3),
	})
	require.NoError(t, err)
	writes := storage.createAttempts

	// note: "claim" repeated, "vacant" never bound, intentionally, "missing" never registered
	require.NoError(t, state.Unbind(logger, storage, "claim", "claim2", "claim", "missing", "vacant"))

	require.Equal(t, writes+1, storage.createAttempts)
	require.Equal(t, map[string]claimAllocationCheckpoint{
		"claim":    {CPUs: "0"},                                                               // successfully unbound, duplicated entry ignored
		"claim2":   {CPUs: "1"},                                                               // successfully unbound
		"vacant":   {CPUs: "2"},                                                               // never bound
		"sentinel": {CPUs: "3", Owner: OwnerIdent{PodUID: "pod", ContainerName: "container"}}, // still bound
	}, readAllocationCheckpoint(t, storage).Payload.ClaimInfo)

	for uid, want := range allocations {
		cpus, ok := state.GetResourceClaimAllocation(uid)
		require.True(t, ok)
		require.True(t, want.Equals(cpus))
		require.Equal(t, uid == "sentinel", state.IsOwner(uid, "pod", "container"))
	}
	require.Equal(t, "0-3", state.GetPreparedCPUs().String())
	require.Empty(t, state.GetSharedCPUs().String())
}

func TestAllocationStateReleaseKeepsStateWhenCheckpointWriteFails(t *testing.T) {
	logger := testr.New(t)
	storage := newRecordingCheckpointManager(t)
	allCPUs := cpuset.New(0, 1, 2, 3)
	store := newTestAllocationState(t, logger, allCPUs, cpuset.New())

	require.NoError(t, store.Reserve(logger, storage, ClaimIdent{UID: "claim"}, cpuset.New(0), false))
	checkpointBefore := readAllocationCheckpoint(t, storage)
	createAttemptsBefore := storage.createAttempts
	allocStateBefore := store.Clone()

	writeErr := errors.New("checkpoint write failed")
	storage.failingMgr = failingCheckpointManager{
		err: writeErr,
	}

	err := store.Release(logger, storage, "claim")
	require.ErrorIs(t, err, writeErr)

	require.Equal(t, createAttemptsBefore+1, storage.createAttempts)
	require.Equal(t, checkpointBefore, readAllocationCheckpoint(t, storage))
	require.Equal(t, allocStateBefore.Snapshot(), store.Snapshot())
	_, ok := store.GetResourceClaimAllocation("claim")
	require.True(t, ok)
}

func TestAllocationStateReleaseKeepsStateWhenCheckpointWriteFailsAfterBindUnbindPair(t *testing.T) {
	logger := testr.New(t)
	storage := newRecordingCheckpointManager(t)
	allCPUs := cpuset.New(0, 1, 2, 3)
	store := newTestAllocationState(t, logger, allCPUs, cpuset.New())

	require.NoError(t, store.Reserve(logger, storage, ClaimIdent{UID: "claim"}, cpuset.New(0), false))
	updatedClaims, err := store.Bind(logger, storage, "pod", "container", map[types.UID]cpuset.CPUSet{"claim": cpuset.New(0)})
	require.NoError(t, err)
	require.Equal(t, []types.UID{"claim"}, updatedClaims)
	require.True(t, store.IsOwner("claim", "pod", "container"))
	require.Equal(t, map[string]claimAllocationCheckpoint{
		"claim": {CPUs: "0", Owner: OwnerIdent{PodUID: "pod", ContainerName: "container"}},
	}, readAllocationCheckpoint(t, storage).Payload.ClaimInfo)

	require.NoError(t, store.Unbind(logger, storage, updatedClaims...))
	require.False(t, store.IsOwner("claim", "pod", "container"))
	require.Equal(t, map[string]claimAllocationCheckpoint{
		"claim": {CPUs: "0"},
	}, readAllocationCheckpoint(t, storage).Payload.ClaimInfo)

	// reserve + bind + unbind = 3
	require.Equal(t, 3, storage.createAttempts)
	checkpointBefore := readAllocationCheckpoint(t, storage)
	createAttemptsBefore := storage.createAttempts
	allocStateBefore := store.Clone()

	writeErr := errors.New("checkpoint write failed")
	storage.failingMgr = failingCheckpointManager{
		err: writeErr,
	}

	err = store.Release(logger, storage, "claim")
	require.ErrorIs(t, err, writeErr)

	require.Equal(t, createAttemptsBefore+1, storage.createAttempts)
	require.Equal(t, checkpointBefore, readAllocationCheckpoint(t, storage))
	require.Equal(t, allocStateBefore.Snapshot(), store.Snapshot())
	cpus, ok := store.GetResourceClaimAllocation("claim")
	require.True(t, ok)
	require.Equal(t, "0", cpus.String())
	require.False(t, store.IsOwner("claim", "pod", "container"))
	require.Equal(t, "1-3", store.GetSharedCPUs().String())
}

func TestAllocationStateRepeatedMutationsAreNoOpaAndDoNotWriteCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(logr.Logger, *AllocationState, checkpoint.Manager) error
	}{
		{
			name: "reserve existing",
			mutate: func(l logr.Logger, s *AllocationState, m checkpoint.Manager) error {
				return s.Reserve(l, m, ClaimIdent{UID: "vacant"}, cpuset.New(0), false)
			},
		},
		{
			name: "bind existing owner",
			mutate: func(l logr.Logger, s *AllocationState, m checkpoint.Manager) error {
				updated, err := s.Bind(l, m, "pod", "app", map[types.UID]cpuset.CPUSet{"owned": cpuset.New(1)})
				if len(updated) != 0 {
					return fmt.Errorf("no-op Bind returned newly owned claims: %v", updated)
				}
				return err
			},
		},
		{
			name: "release missing",
			mutate: func(l logr.Logger, s *AllocationState, m checkpoint.Manager) error {
				return s.Release(l, m, "missing")
			},
		},
		{
			name: "unbind no arguments",
			mutate: func(l logr.Logger, s *AllocationState, m checkpoint.Manager) error {
				return s.Unbind(l, m)
			},
		},
		{
			name: "unbind missing",
			mutate: func(l logr.Logger, s *AllocationState, m checkpoint.Manager) error {
				return s.Unbind(l, m, "missing")
			},
		},
		{
			name: "unbind vacant",
			mutate: func(l logr.Logger, s *AllocationState, m checkpoint.Manager) error {
				return s.Unbind(l, m, "vacant")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := testr.New(t)
			manager := newRecordingCheckpointManager(t)
			state := newTestAllocationState(t, logger, cpuset.New(0, 1, 2, 3), cpuset.New())
			require.NoError(t, state.Reserve(logger, manager, ClaimIdent{UID: "vacant"}, cpuset.New(0), false))
			require.NoError(t, state.Reserve(logger, manager, ClaimIdent{UID: "owned"}, cpuset.New(1), false))
			_, err := state.Bind(logger, manager, "pod", "app", map[types.UID]cpuset.CPUSet{"owned": cpuset.New(1)})
			require.NoError(t, err)

			before := readAllocationCheckpoint(t, manager)
			createAttemptsBefore := manager.createAttempts

			require.NoError(t, tc.mutate(logger, state, manager))
			require.Equal(t, createAttemptsBefore, manager.createAttempts)
			require.Equal(t, before, readAllocationCheckpoint(t, manager))
			require.True(t, state.IsOwner("owned", "pod", "app"))
			require.False(t, state.IsOwner("vacant", "pod", "app"))
			require.Equal(t, "0-1", state.GetPreparedCPUs().String())
			require.Equal(t, "2-3", state.GetSharedCPUs().String())
			require.Equal(t, 2, state.Snapshot().ActiveResourceClaims)
			cpus, found := state.GetResourceClaimAllocation("vacant")
			require.True(t, found)
			require.Equal(t, "0", cpus.String())

			cpus, found = state.GetResourceClaimAllocation("owned")
			require.True(t, found)
			require.Equal(t, "1", cpus.String())
		})
	}
}

func TestAllocationStateDetectsAndFailsWithNilManager(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(logr.Logger, *AllocationState) error
	}{
		{
			name: "load",
			mutate: func(l logr.Logger, s *AllocationState) error {
				return s.LoadState(l, nil)
			},
		},
		{
			name: "reserve",
			mutate: func(l logr.Logger, s *AllocationState) error {
				return s.Reserve(l, nil, ClaimIdent{UID: "new"}, cpuset.New(2), false)
			},
		},
		{
			name: "bind",
			mutate: func(l logr.Logger, s *AllocationState) error {
				_, err := s.Bind(l, nil, "pod", "app", map[types.UID]cpuset.CPUSet{"vacant": cpuset.New(0)})
				return err
			},
		},
		{
			name: "unbind",
			mutate: func(l logr.Logger, s *AllocationState) error {
				return s.Unbind(l, nil, "owned")
			},
		},
		{
			name: "release",
			mutate: func(l logr.Logger, s *AllocationState) error {
				return s.Release(l, nil, "owned")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := testr.New(t)
			manager := newRecordingCheckpointManager(t)
			state := newTestAllocationState(t, logger, cpuset.New(0, 1, 2, 3), cpuset.New())
			require.NoError(t, state.Reserve(logger, manager, ClaimIdent{UID: "vacant"}, cpuset.New(0), false))
			require.NoError(t, state.Reserve(logger, manager, ClaimIdent{UID: "owned"}, cpuset.New(1), false))
			_, err := state.Bind(logger, manager, "pod", "app", map[types.UID]cpuset.CPUSet{"owned": cpuset.New(1)})
			require.NoError(t, err)
			checkpointBefore := readAllocationCheckpoint(t, manager)
			createAttemptsBefore := manager.createAttempts

			require.ErrorIs(t, tc.mutate(logger, state), ErrNilCheckpointManager)

			require.Equal(t, createAttemptsBefore, manager.createAttempts)
			require.Equal(t, checkpointBefore, readAllocationCheckpoint(t, manager))
			require.True(t, state.IsOwner("owned", "pod", "app"))
			require.False(t, state.IsOwner("vacant", "pod", "app"))
			require.Equal(t, "0-1", state.GetPreparedCPUs().String())
			require.Equal(t, "2-3", state.GetSharedCPUs().String())
			require.Equal(t, AllocationSnapshot{AllocatedCPUs: 2, AvailableCPUs: 2, ActiveResourceClaims: 2}, state.Snapshot())
			cpus, found := state.GetResourceClaimAllocation("vacant")
			require.True(t, found)
			require.Equal(t, "0", cpus.String())

			cpus, found = state.GetResourceClaimAllocation("owned")
			require.True(t, found)
			require.Equal(t, "1", cpus.String())
		})
	}
}

func TestAllocationStatePersistsReserveAndRelease(t *testing.T) {
	logger := testr.New(t)
	storage := newRecordingCheckpointManager(t)
	state := newTestAllocationState(t, logger, cpuset.New(0, 1, 2, 3), cpuset.New())
	// add a sentinel with an owner unrelated to the exercised and which is preserved until the final release empties the checkpoint
	require.NoError(t, state.Reserve(logger, storage, ClaimIdent{UID: "sentinel"}, cpuset.New(3), false))
	_, err := state.Bind(logger, storage, "other-pod", "other-app", map[types.UID]cpuset.CPUSet{"sentinel": cpuset.New(3)})
	require.NoError(t, err)

	for _, tc := range []struct {
		name     string
		reserve  bool
		bind     bool
		uid      types.UID
		want     map[string]claimAllocationCheckpoint
		prepared string
		shared   string
	}{
		{
			name:    "reserve preserves unrelated owner",
			reserve: true,
			uid:     "claim",
			want: map[string]claimAllocationCheckpoint{
				"claim":    {CPUs: "0-1"},
				"sentinel": {CPUs: "3", Owner: OwnerIdent{PodUID: "other-pod", ContainerName: "other-app"}},
			},
			prepared: "0-1,3",
			shared:   "2",
		},
		{
			name: "release preserves unrelated owner",
			bind: true,
			uid:  "claim",
			want: map[string]claimAllocationCheckpoint{
				"sentinel": {CPUs: "3", Owner: OwnerIdent{PodUID: "other-pod", ContainerName: "other-app"}},
			},
			prepared: "3",
			shared:   "0-2",
		},
		{
			name:     "release final owned claim",
			uid:      "sentinel",
			want:     map[string]claimAllocationCheckpoint{},
			prepared: "",
			shared:   "0-3",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.bind {
				_, err := state.Bind(logger, storage, "pod", "app", map[types.UID]cpuset.CPUSet{"claim": cpuset.New(0, 1)})
				require.NoError(t, err)
			}
			writes := storage.createAttempts
			if tc.reserve {
				require.NoError(t, state.Reserve(logger, storage, ClaimIdent{UID: tc.uid}, cpuset.New(0, 1), false))
			} else {
				require.NoError(t, state.Release(logger, storage, tc.uid))
				_, ok := state.GetResourceClaimAllocation(tc.uid)
				require.False(t, ok)
				require.False(t, state.IsOwner(tc.uid, "other-pod", "other-app"))
				require.False(t, state.IsOwner(tc.uid, "pod", "app"))
			}
			require.Equal(t, writes+1, storage.createAttempts)
			require.Equal(t, tc.want, readAllocationCheckpoint(t, storage).Payload.ClaimInfo)
			for uid, want := range tc.want {
				cpus, ok := state.GetResourceClaimAllocation(types.UID(uid))
				require.True(t, ok)
				require.Equal(t, want.CPUs, cpus.String())
			}
			require.Equal(t, tc.prepared, state.GetPreparedCPUs().String())
			require.Equal(t, tc.shared, state.GetSharedCPUs().String())
			require.Equal(t, len(tc.want), state.Snapshot().ActiveResourceClaims)
			require.Equal(t, tc.uid != "sentinel", state.IsOwner("sentinel", "other-pod", "other-app"))
		})
	}
}

func TestAllocationStateRejectedReserveDoesNotWriteCheckpoint(t *testing.T) {
	for _, tc := range []struct {
		name      string
		claim     ClaimIdent
		cpus      cpuset.CPUSet
		wantError string
	}{
		{
			name:      "overlaps another claim",
			claim:     ClaimIdent{UID: "claim"},
			wantError: "overlapping CPU assignment",
			cpus:      cpuset.New(0, 1),
		},
		{
			name:      "overlaps reserved CPU",
			claim:     ClaimIdent{UID: "claim"},
			wantError: "overlapping CPU assignment",
			cpus:      cpuset.New(3),
		},
		{
			name:      "CPU outside topology",
			claim:     ClaimIdent{UID: "claim"},
			wantError: "overlapping CPU assignment",
			cpus:      cpuset.New(4),
		},
		{
			name:      "empty claim UID",
			claim:     ClaimIdent{},
			cpus:      cpuset.New(1, 2),
			wantError: "invalid claim identifier",
		},
		{
			name:      "empty cpuset",
			claim:     ClaimIdent{UID: "claim"},
			cpus:      cpuset.New(),
			wantError: "has no CPUs",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := testr.New(t)
			storage := newRecordingCheckpointManager(t)
			state := newTestAllocationState(t, logger, cpuset.New(0, 1, 2, 3), cpuset.New(3))
			// add a sentinel which pre-exists and is not mutated by the binding under tests, so we check no unrelated overwrites
			require.NoError(t, state.Reserve(logger, storage, ClaimIdent{UID: "sentinel"}, cpuset.New(0), false))
			_, err := state.Bind(logger, storage, "pod", "app", map[types.UID]cpuset.CPUSet{"sentinel": cpuset.New(0)})
			require.NoError(t, err)

			before := readAllocationCheckpoint(t, storage)
			writes := storage.createAttempts

			require.ErrorContains(t, state.Reserve(logger, storage, tc.claim, tc.cpus, false), tc.wantError)
			require.Equal(t, writes, storage.createAttempts)
			require.Equal(t, before, readAllocationCheckpoint(t, storage))

			_, ok := state.GetResourceClaimAllocation("claim")
			require.False(t, ok)

			cpus, ok := state.GetResourceClaimAllocation("sentinel")
			require.True(t, ok)
			require.Equal(t, "0", cpus.String())
			require.True(t, state.IsOwner("sentinel", "pod", "app"))
			require.Equal(t, "0", state.GetPreparedCPUs().String())
			require.Equal(t, "1-2", state.GetSharedCPUs().String())
			require.Equal(t, AllocationSnapshot{AllocatedCPUs: 1, AvailableCPUs: 2, ReservedCPUs: 1, ActiveResourceClaims: 1}, state.Snapshot())
		})
	}
}

func TestAllocationStateLoadStateDoesNotPartiallyApply(t *testing.T) {
	for _, tc := range []struct {
		name             string
		sentinel         *claimAllocationCheckpoint
		injectedClaimUID string
		injectedCPUs     string
		wantError        string
	}{
		{
			name:             "empty state, malformed record",
			injectedClaimUID: "claim-b",
			injectedCPUs:     "invalid", // will cause parsing error
			wantError:        "restoring claim \"claim-b\"",
		},
		{
			name: "populated state, malformed record",
			sentinel: &claimAllocationCheckpoint{
				Claim: NamespacedName{
					Namespace: "original-ns",
					Name:      "sentinel",
				},
				CPUs: "3",
				Owner: OwnerIdent{
					PodUID:        "original-pod",
					ContainerName: "original-app",
				},
			},
			injectedClaimUID: "claim-b",
			injectedCPUs:     "invalid", // will cause parsing error
			wantError:        "restoring claim \"claim-b\"",
		},
		{
			name:             "empty state, overlapping records",
			injectedClaimUID: "claim-b",
			injectedCPUs:     "1-2", // cpu 1 overlaps
			wantError:        "overlap with already prepared CPUs",
		},
		{
			name: "populated state, overlapping records",
			sentinel: &claimAllocationCheckpoint{
				Claim: NamespacedName{
					Namespace: "original-ns",
					Name:      "sentinel",
				},
				CPUs: "3",
				Owner: OwnerIdent{
					PodUID:        "original-pod",
					ContainerName: "original-app",
				},
			},
			injectedClaimUID: "claim-b",
			injectedCPUs:     "1-2", // cpu 1 overlaps
			wantError:        "overlap with already prepared CPUs",
		},
		{
			name: "populated state, attempted replace of existing claim",
			sentinel: &claimAllocationCheckpoint{
				Claim: NamespacedName{
					Namespace: "original-ns",
					Name:      "sentinel",
				},
				CPUs: "3",
				Owner: OwnerIdent{
					PodUID:        "original-pod",
					ContainerName: "original-app",
				},
			},
			injectedClaimUID: "sentinel", // attempted replace
			injectedCPUs:     "3",        // overlaps the target's existing sentinel allocation
			wantError:        "overlap with already prepared CPUs",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := testr.New(t)
			storage := newRecordingCheckpointManager(t)
			allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
			reservedCPUs := cpuset.New(4)
			source := newTestAllocationState(t, logger, allCPUs, reservedCPUs)
			claim := ClaimIdent{
				UID: "claim-a",
				Claim: NamespacedName{
					Namespace: "ns",
					Name:      "claim-a",
				},
			}
			require.NoError(t, source.Reserve(logger, storage, claim, cpuset.New(0, 1), false))
			_, err := source.Bind(logger, storage, "pod-a", "app", map[types.UID]cpuset.CPUSet{"claim-a": cpuset.New(0, 1)})
			require.NoError(t, err)

			data := readAllocationCheckpoint(t, storage)
			data.Payload.ClaimInfo[tc.injectedClaimUID] = claimAllocationCheckpoint{
				Claim: NamespacedName{
					Namespace: "injected-ns",
					Name:      tc.injectedClaimUID,
				},
				CPUs: tc.injectedCPUs,
				Owner: OwnerIdent{
					PodUID:        "other-pod",
					ContainerName: "other-app",
				},
			}
			require.NoError(t, storage.CreateCheckpoint(allocationCheckpointName, &AllocationStateCheckpoint{checkpointData: data}))
			checkpointBefore := readAllocationCheckpoint(t, storage)
			createAttemptsBefore := storage.createAttempts

			target := newTestAllocationState(t, logger, allCPUs, reservedCPUs)
			wantSnapshot := AllocationSnapshot{
				AvailableCPUs: 7,
				ReservedCPUs:  1,
			}
			wantPrepared := ""
			wantShared := "0-3,5-7"
			if tc.sentinel != nil {
				cpus, err := cpuset.Parse(tc.sentinel.CPUs)
				require.NoError(t, err)
				sentinel := ClaimIdent{
					UID:   "sentinel",
					Claim: tc.sentinel.Claim,
				}
				require.NoError(t, target.Reserve(logger, noop, sentinel, cpus, false))
				_, err = target.Bind(logger, noop, tc.sentinel.Owner.PodUID, tc.sentinel.Owner.ContainerName, map[types.UID]cpuset.CPUSet{sentinel.UID: cpus})
				require.NoError(t, err)
				wantSnapshot = AllocationSnapshot{
					AllocatedCPUs:        1,
					AvailableCPUs:        6,
					ReservedCPUs:         1,
					ActiveResourceClaims: 1,
				}
				wantPrepared = "3"
				wantShared = "0-2,5-7"
			}

			err = target.LoadState(logger, storage)
			require.ErrorContains(t, err, tc.wantError)
			require.Equal(t, wantSnapshot, target.Snapshot())
			require.Equal(t, wantPrepared, target.GetPreparedCPUs().String())
			require.Equal(t, wantShared, target.GetSharedCPUs().String())
			require.Equal(t, "4", target.GetReservedCPUs().String())
			_, found := target.GetResourceClaimAllocation("claim-a")
			require.False(t, found)
			_, found = target.GetResourceClaimAllocation("claim-b")
			require.False(t, found)
			cpus, found := target.GetResourceClaimAllocation("sentinel")
			require.Equal(t, tc.sentinel != nil, found)
			if tc.sentinel != nil {
				require.Equal(t, "3", cpus.String())
				require.True(t, target.IsOwner("sentinel", "original-pod", "original-app"))
				require.False(t, target.IsOwner("sentinel", "other-pod", "other-app"))
			}
			require.Equal(t, createAttemptsBefore, storage.createAttempts)
			require.Equal(t, checkpointBefore, readAllocationCheckpoint(t, storage))
		})
	}
}

func TestAllocationStateBindRejectsInvalidOwner(t *testing.T) {
	for _, tc := range []struct {
		name          string
		podUID        types.UID
		containerName string
	}{
		{
			name: "vacant owner",
		},
		{
			name:          "empty pod UID",
			containerName: "app",
		},
		{
			name:   "empty container name",
			podUID: "pod",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := testr.New(t)
			manager := newRecordingCheckpointManager(t)
			state := newTestAllocationState(t, logger, cpuset.New(0, 1, 2, 3), cpuset.New())
			require.NoError(t, state.Reserve(logger, manager, ClaimIdent{UID: "vacant"}, cpuset.New(0, 1), false))

			// unrelated reserved and bound claim which must not be altered by the test
			require.NoError(t, state.Reserve(logger, manager, ClaimIdent{UID: "owned"}, cpuset.New(2, 3), false))
			_, err := state.Bind(logger, manager, "other-pod", "other-app", map[types.UID]cpuset.CPUSet{"owned": cpuset.New(2, 3)})
			require.NoError(t, err)
			before := readAllocationCheckpoint(t, manager)
			writes := manager.createAttempts

			updated, err := state.Bind(logger, manager, tc.podUID, tc.containerName, map[types.UID]cpuset.CPUSet{"vacant": cpuset.New(0, 1)})

			require.ErrorContains(t, err, "provide both a pod UID and container name")
			require.Empty(t, updated)
			require.Equal(t, writes, manager.createAttempts)
			require.Equal(t, before, readAllocationCheckpoint(t, manager))
			require.False(t, state.IsOwner("vacant", tc.podUID, tc.containerName))
			require.True(t, state.IsOwner("owned", "other-pod", "other-app"))

			cpus, ok := state.GetResourceClaimAllocation("vacant")
			require.True(t, ok)
			require.Equal(t, "0-1", cpus.String())

			cpus, ok = state.GetResourceClaimAllocation("owned")
			require.True(t, ok)
			require.Equal(t, "2-3", cpus.String())

			require.Equal(t, "0-3", state.GetPreparedCPUs().String())
			require.Empty(t, state.GetSharedCPUs().String())
			require.Equal(t, 2, state.Snapshot().ActiveResourceClaims)
		})
	}
}
