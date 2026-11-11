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
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/checkpoint"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
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
	removeAttempts int
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

func (m *recordingCheckpointManager) RemoveCheckpoint(name string) error {
	m.removeAttempts++
	return m.Manager.RemoveCheckpoint(name)
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
	require.NoError(t, store.Reserve(logger, storage, ClaimIdentifier{UID: "sentinel"}, cpuset.New(3), false))
	checkpointBefore := readAllocationCheckpoint(t, storage)
	createAttemptsBefore := storage.createAttempts
	allocStateBefore := store.Clone()

	manager := failingCheckpointManager{
		err: errors.New("checkpoint write failed"),
	}

	storage.failingMgr = manager
	err := store.Reserve(logger, storage, ClaimIdentifier{UID: "claim"}, cpuset.New(0), false)
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

	require.NoError(t, store.Reserve(logger, storage, ClaimIdentifier{UID: "claim"}, cpuset.New(0), false))
	require.NoError(t, store.Reserve(logger, storage, ClaimIdentifier{UID: "second"}, cpuset.New(1), false))

	// add a sentinel which pre-exists and is not mutated by the binding under tests, so we check no unrelated overwrites
	require.NoError(t, store.Reserve(logger, storage, ClaimIdentifier{UID: "sentinel"}, cpuset.New(3), false))
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

	require.NoError(t, store.Reserve(logger, storage, ClaimIdentifier{UID: "claim"}, cpuset.New(0), false))
	_, err := store.Bind(logger, storage, "pod", "container", map[types.UID]cpuset.CPUSet{"claim": cpuset.New(0)})
	require.NoError(t, err)
	checkpointBefore := readAllocationCheckpoint(t, storage)
	createAttemptsBefore := storage.createAttempts
	allocStateBefore := store.Clone()

	writeErr := errors.New("checkpoint write failed")
	storage.failingMgr = failingCheckpointManager{
		err: writeErr,
	}

	err = store.Unbind(logger, storage, "claim")
	require.ErrorContains(t, err, "checkpoint write failed")
	require.ErrorIs(t, err, writeErr)

	require.Equal(t, createAttemptsBefore+1, storage.createAttempts)
	require.Equal(t, checkpointBefore, readAllocationCheckpoint(t, storage))
	require.Equal(t, allocStateBefore.resourceClaimAllocations, store.resourceClaimAllocations)
	require.True(t, store.IsOwner("claim", "pod", "container"))
}

func TestAllocationStateUnbindBatchKeepsStateWhenCheckpointWriteFails(t *testing.T) {
	logger := testr.New(t)
	storage := newRecordingCheckpointManager(t)
	state := newTestAllocationState(t, logger, cpuset.New(0, 1, 2, 3), cpuset.New())

	// add a sentinel which pre-exists and is not mutated by the unbinding being exercised, so we check no unrelated overwrites
	require.NoError(t, state.Reserve(logger, storage, ClaimIdentifier{UID: "sentinel"}, cpuset.New(3), false))
	_, err := state.Bind(logger, storage, "other-pod", "other-app", map[types.UID]cpuset.CPUSet{
		"sentinel": cpuset.New(3),
	})
	require.NoError(t, err)

	require.NoError(t, state.Reserve(logger, storage, ClaimIdentifier{UID: "first"}, cpuset.New(0), false))
	require.NoError(t, state.Reserve(logger, storage, ClaimIdentifier{UID: "second"}, cpuset.New(1), false))
	_, err = state.Bind(logger, storage, "pod", "container", map[types.UID]cpuset.CPUSet{
		"first":  cpuset.New(0),
		"second": cpuset.New(1),
	})
	require.NoError(t, err)
	wantClaims := map[string]claimAllocationCheckpoint{
		"first":    {CPUs: "0", Owner: ownerIdentCheckpoint{PodUID: "pod", ContainerName: "container"}},
		"second":   {CPUs: "1", Owner: ownerIdentCheckpoint{PodUID: "pod", ContainerName: "container"}},
		"sentinel": {CPUs: "3", Owner: ownerIdentCheckpoint{PodUID: "other-pod", ContainerName: "other-app"}},
	}
	checkpointBefore := readAllocationCheckpoint(t, storage)
	require.Equal(t, wantClaims, checkpointBefore.Payload.ClaimInfo)
	writes := storage.createAttempts

	writeErr := errors.New("checkpoint write failed")
	storage.failingMgr = failingCheckpointManager{
		err: writeErr,
	}

	err = state.Unbind(logger, storage, "first", "second")
	require.ErrorIs(t, err, writeErr)
	// one attempt
	require.Equal(t, writes+1, storage.createAttempts)
	// but no changes
	require.Equal(t, checkpointBefore, readAllocationCheckpoint(t, storage))

	// check no changes, in detail
	cpus, ok := state.GetResourceClaimAllocation("first")
	require.True(t, ok)
	require.Equal(t, "0", cpus.String())
	require.True(t, state.IsOwner("first", "pod", "container"))

	cpus, ok = state.GetResourceClaimAllocation("second")
	require.True(t, ok)
	require.Equal(t, "1", cpus.String())
	require.True(t, state.IsOwner("second", "pod", "container"))

	cpus, ok = state.GetResourceClaimAllocation("sentinel")
	require.True(t, ok)
	require.Equal(t, "3", cpus.String())
	require.True(t, state.IsOwner("sentinel", "other-pod", "other-app"))

	require.Equal(t, "0-1,3", state.GetPreparedCPUs().String())
	require.Equal(t, "2", state.GetSharedCPUs().String())
	require.Equal(t, AllocationSnapshot{AllocatedCPUs: 3, AvailableCPUs: 1, ActiveResourceClaims: 3}, state.Snapshot())
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
		require.NoError(t, state.Reserve(logger, storage, ClaimIdentifier{UID: uid}, cpus, false))
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
		"claim":    {CPUs: "0"},                                                                         // successfully unbound, duplicated entry ignored
		"claim2":   {CPUs: "1"},                                                                         // successfully unbound
		"vacant":   {CPUs: "2"},                                                                         // never bound
		"sentinel": {CPUs: "3", Owner: ownerIdentCheckpoint{PodUID: "pod", ContainerName: "container"}}, // still bound
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

	require.NoError(t, store.Reserve(logger, storage, ClaimIdentifier{UID: "claim"}, cpuset.New(0), false))
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

	require.NoError(t, store.Reserve(logger, storage, ClaimIdentifier{UID: "claim"}, cpuset.New(0), false))
	updatedClaims, err := store.Bind(logger, storage, "pod", "container", map[types.UID]cpuset.CPUSet{"claim": cpuset.New(0)})
	require.NoError(t, err)
	require.Equal(t, []types.UID{"claim"}, updatedClaims)
	require.True(t, store.IsOwner("claim", "pod", "container"))
	require.Equal(t, map[string]claimAllocationCheckpoint{
		"claim": {CPUs: "0", Owner: ownerIdentCheckpoint{PodUID: "pod", ContainerName: "container"}},
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
				return s.Reserve(l, m, ClaimIdentifier{UID: "vacant"}, cpuset.New(0), false)
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
			require.NoError(t, state.Reserve(logger, manager, ClaimIdentifier{UID: "vacant"}, cpuset.New(0), false))
			require.NoError(t, state.Reserve(logger, manager, ClaimIdentifier{UID: "owned"}, cpuset.New(1), false))
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
				return s.Reserve(l, nil, ClaimIdentifier{UID: "new"}, cpuset.New(2), false)
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
			require.NoError(t, state.Reserve(logger, manager, ClaimIdentifier{UID: "vacant"}, cpuset.New(0), false))
			require.NoError(t, state.Reserve(logger, manager, ClaimIdentifier{UID: "owned"}, cpuset.New(1), false))
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
	require.NoError(t, state.Reserve(logger, storage, ClaimIdentifier{UID: "sentinel"}, cpuset.New(3), false))
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
				"sentinel": {CPUs: "3", Owner: ownerIdentCheckpoint{PodUID: "other-pod", ContainerName: "other-app"}},
			},
			prepared: "0-1,3",
			shared:   "2",
		},
		{
			name: "release preserves unrelated owner",
			bind: true,
			uid:  "claim",
			want: map[string]claimAllocationCheckpoint{
				"sentinel": {CPUs: "3", Owner: ownerIdentCheckpoint{PodUID: "other-pod", ContainerName: "other-app"}},
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
				require.NoError(t, state.Reserve(logger, storage, ClaimIdentifier{UID: tc.uid}, cpuset.New(0, 1), false))
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
		claim     ClaimIdentifier
		cpus      cpuset.CPUSet
		wantError string
	}{
		{
			name:      "overlaps another claim",
			claim:     ClaimIdentifier{UID: "claim"},
			wantError: "overlapping CPU assignment",
			cpus:      cpuset.New(0, 1),
		},
		{
			name:      "overlaps reserved CPU",
			claim:     ClaimIdentifier{UID: "claim"},
			wantError: "overlapping CPU assignment",
			cpus:      cpuset.New(3),
		},
		{
			name:      "CPU outside topology",
			claim:     ClaimIdentifier{UID: "claim"},
			wantError: "overlapping CPU assignment",
			cpus:      cpuset.New(4),
		},
		{
			name:      "empty claim UID",
			claim:     ClaimIdentifier{},
			cpus:      cpuset.New(1, 2),
			wantError: "invalid claim identifier",
		},
		{
			name:      "empty cpuset",
			claim:     ClaimIdentifier{UID: "claim"},
			cpus:      cpuset.New(),
			wantError: "has no CPUs",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := testr.New(t)
			storage := newRecordingCheckpointManager(t)
			state := newTestAllocationState(t, logger, cpuset.New(0, 1, 2, 3), cpuset.New(3))
			// add a sentinel which pre-exists and is not mutated by the binding under tests, so we check no unrelated overwrites
			require.NoError(t, state.Reserve(logger, storage, ClaimIdentifier{UID: "sentinel"}, cpuset.New(0), false))
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
				Claim: namespacedNameCheckpoint{
					Namespace: "original-ns",
					Name:      "sentinel",
				},
				CPUs: "3",
				Owner: ownerIdentCheckpoint{
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
				Claim: namespacedNameCheckpoint{
					Namespace: "original-ns",
					Name:      "sentinel",
				},
				CPUs: "3",
				Owner: ownerIdentCheckpoint{
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
				Claim: namespacedNameCheckpoint{
					Namespace: "original-ns",
					Name:      "sentinel",
				},
				CPUs: "3",
				Owner: ownerIdentCheckpoint{
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
			claim := ClaimIdentifier{
				UID: "claim-a",
				Claim: types.NamespacedName{
					Namespace: "ns",
					Name:      "claim-a",
				},
			}
			require.NoError(t, source.Reserve(logger, storage, claim, cpuset.New(0, 1), false))
			_, err := source.Bind(logger, storage, "pod-a", "app", map[types.UID]cpuset.CPUSet{"claim-a": cpuset.New(0, 1)})
			require.NoError(t, err)

			data := readAllocationCheckpoint(t, storage)
			data.Payload.ClaimInfo[tc.injectedClaimUID] = claimAllocationCheckpoint{
				Claim: namespacedNameCheckpoint{
					Namespace: "injected-ns",
					Name:      tc.injectedClaimUID,
				},
				CPUs: tc.injectedCPUs,
				Owner: ownerIdentCheckpoint{
					PodUID:        "other-pod",
					ContainerName: "other-app",
				},
			}
			require.NoError(t, storage.CreateCheckpoint(allocationCheckpointName, &AllocationStateCheckpoint{checkpointData: data}))
			checkpointBefore := readAllocationCheckpoint(t, storage)
			createAttemptsBefore := storage.createAttempts
			removeAttemptsBefore := storage.removeAttempts
			path := filepath.Join(storage.CheckpointPath(), allocationCheckpointName)
			fileBefore, err := os.ReadFile(path)
			require.NoError(t, err)

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
				sentinel := ClaimIdentifier{
					UID: "sentinel",
					Claim: types.NamespacedName{
						Namespace: tc.sentinel.Claim.Namespace,
						Name:      tc.sentinel.Claim.Name,
					},
				}
				require.NoError(t, target.Reserve(logger, noop, sentinel, cpus, false))
				_, err = target.Bind(logger, noop, types.UID(tc.sentinel.Owner.PodUID), tc.sentinel.Owner.ContainerName, map[types.UID]cpuset.CPUSet{sentinel.UID: cpus})
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
			require.Equal(t, removeAttemptsBefore, storage.removeAttempts)
			fileAfter, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, fileBefore, fileAfter)
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
			require.NoError(t, state.Reserve(logger, manager, ClaimIdentifier{UID: "vacant"}, cpuset.New(0, 1), false))

			// unrelated reserved and bound claim which must not be altered by the test
			require.NoError(t, state.Reserve(logger, manager, ClaimIdentifier{UID: "owned"}, cpuset.New(2, 3), false))
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

func newLoadStateTestTopology() *cpuinfo.CPUTopology {
	// no SMT
	return &cpuinfo.CPUTopology{
		NumCPUs:      4,
		NumCores:     4,
		NumSockets:   1,
		NumNUMANodes: 1,
		CPUDetails: cpuinfo.CPUDetails{
			0: {
				CpuID:      0,
				CoreID:     0,
				SocketID:   0,
				NUMANodeID: 0,
			},
			1: {
				CpuID:      1,
				CoreID:     1,
				SocketID:   0,
				NUMANodeID: 0,
			},
			2: {
				CpuID:      2,
				CoreID:     2,
				SocketID:   0,
				NUMANodeID: 0,
			},
			3: {
				CpuID:      3,
				CoreID:     3,
				SocketID:   0,
				NUMANodeID: 0,
			},
		},
	}
}

func TestAllocationStateLoadStateConsistency(t *testing.T) {
	topology := newLoadStateTestTopology()

	for _, tc := range []struct {
		name        string
		mutate      func(*allocationStateCheckpointData)
		wantErr     error
		wantErrText string
	}{
		{
			name: "wrong topology",
			mutate: func(data *allocationStateCheckpointData) {
				data.Meta.TopologyFingerprint = "reandom-junk"
			},
			wantErr: ErrTopologyChanged,
		},
		{
			name: "wrong node",
			mutate: func(data *allocationStateCheckpointData) {
				data.Meta.NodeName = "other-node"
			},
			wantErr: ErrNodeChanged,
		},
		{
			name: "unsupported version",
			mutate: func(data *allocationStateCheckpointData) {
				data.Meta.Version = 2
			},
			wantErrText: "unsupported allocation state checkpoint version 2",
		},
		{
			name: "unparsable reserved CPUs",
			mutate: func(data *allocationStateCheckpointData) {
				data.Meta.ReservedCPUs = "invalid"
			},
			wantErrText: "cannot recover checkpointed reserved cpus",
		},
		{
			name: "empty claim UID",
			mutate: func(data *allocationStateCheckpointData) {
				data.Payload.ClaimInfo[""] = data.Payload.ClaimInfo["owned"]
				delete(data.Payload.ClaimInfo, "owned")
			},
			wantErrText: "restoring claim: missing ident",
		},
		{
			name: "empty claim CPUs",
			mutate: func(data *allocationStateCheckpointData) {
				claim := data.Payload.ClaimInfo["owned"]
				claim.CPUs = ""
				data.Payload.ClaimInfo["owned"] = claim
			},
			wantErrText: `restoring claim "owned": malformed claim data cpus=`,
		},
		{
			name: "claim CPUs outside topology",
			mutate: func(data *allocationStateCheckpointData) {
				claim := data.Payload.ClaimInfo["owned"]
				claim.CPUs = "4"
				data.Payload.ClaimInfo["owned"] = claim
			},
			wantErrText: "CPUs outside the available set: 4",
		},
		{
			name: "claim uses reserved CPUs",
			mutate: func(data *allocationStateCheckpointData) {
				claim := data.Payload.ClaimInfo["owned"]
				claim.CPUs = "3"
				data.Payload.ClaimInfo["owned"] = claim
			},
			wantErrText: "CPUs outside the available set: 3",
		},
		{
			name: "owner missing pod UID",
			mutate: func(data *allocationStateCheckpointData) {
				claim := data.Payload.ClaimInfo["owned"]
				claim.Owner.PodUID = ""
				data.Payload.ClaimInfo["owned"] = claim
			},
			wantErrText: "claim must be vacant or assigned",
		},
		{
			name: "owner missing container name",
			mutate: func(data *allocationStateCheckpointData) {
				claim := data.Payload.ClaimInfo["owned"]
				claim.Owner.ContainerName = ""
				data.Payload.ClaimInfo["owned"] = claim
			},
			wantErrText: "claim must be vacant or assigned",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := testr.New(t)
			storage := newRecordingCheckpointManager(t)
			chkPath := filepath.Join(storage.CheckpointPath(), allocationCheckpointName)
			data := allocationStateCheckpointData{
				Meta: allocationStateCheckpointMeta{
					Version:             1,
					NodeName:            "test-node-load-state",
					ReservedCPUs:        "3",
					TopologyFingerprint: MakeTopologyFingerprint(*topology),
				},
				Payload: allocationStateCheckpointPayload{
					ClaimInfo: map[string]claimAllocationCheckpoint{
						"vacant": {
							Claim: namespacedNameCheckpoint{
								Namespace: "workloads",
								Name:      "vacant-claim",
							},
							CPUs: "0",
						},
						"owned": {
							Claim: namespacedNameCheckpoint{
								Namespace: "workloads",
								Name:      "owned-claim",
							},
							CPUs: "1",
							Owner: ownerIdentCheckpoint{
								PodUID:        "pod",
								ContainerName: "app",
							},
						},
					},
				},
			}
			tc.mutate(&data)
			require.NoError(t, storage.CreateCheckpoint(allocationCheckpointName, &AllocationStateCheckpoint{
				checkpointData: data,
			}))
			chkBefore, err := os.ReadFile(chkPath)
			require.NoError(t, err)

			// enforce known values
			storage.createAttempts = 0
			storage.removeAttempts = 0

			reservedCPUs := cpuset.New(3)
			sharedCPUs := cpuset.New(0, 1, 2)
			target := NewAllocationState("test-node-load-state", topology, reservedCPUs)
			err = target.LoadState(logger, storage)

			switch {
			case tc.wantErr != nil: // precise sentinel error
				require.ErrorIs(t, err, tc.wantErr)
			case tc.wantErrText != "": // necessary evil of error string matching
				require.ErrorContains(t, err, tc.wantErrText)
			default: // test bug
				t.Fail()
			}

			// loading must not write or remove checkpoints
			require.Zero(t, storage.createAttempts)
			require.Zero(t, storage.removeAttempts)

			chkAfter, err := os.ReadFile(chkPath)
			require.NoError(t, err)
			require.Equal(t, chkBefore, chkAfter)

			// ensure no changes when
			require.Equal(t, reservedCPUs.String(), target.GetReservedCPUs().String())
			require.Equal(t, AllocationSnapshot{
				AvailableCPUs: topology.NumCPUs - reservedCPUs.Size(),
				ReservedCPUs:  reservedCPUs.Size(),
			}, target.Snapshot())
			require.Empty(t, target.GetPreparedCPUs().String())
			require.Equal(t, sharedCPUs.String(), target.GetSharedCPUs().String())
		})
	}
}

func TestAllocationStateLoadStateConsistencyClaims(t *testing.T) {
	topology := newLoadStateTestTopology()
	logger := testr.New(t)
	storage := newRecordingCheckpointManager(t)
	chkPath := filepath.Join(storage.CheckpointPath(), allocationCheckpointName)
	data := allocationStateCheckpointData{
		Meta: allocationStateCheckpointMeta{
			Version:             1,
			NodeName:            "test-node-load-claims",
			ReservedCPUs:        "3",
			TopologyFingerprint: MakeTopologyFingerprint(*topology),
		},
		Payload: allocationStateCheckpointPayload{
			ClaimInfo: map[string]claimAllocationCheckpoint{
				"vacant": {
					Claim: namespacedNameCheckpoint{
						Namespace: "workloads",
						Name:      "vacant-claim",
					},
					CPUs: "0",
				},
				"owned": {
					Claim: namespacedNameCheckpoint{
						Namespace: "workloads",
						Name:      "owned-claim",
					},
					CPUs: "1",
					Owner: ownerIdentCheckpoint{
						PodUID:        "pod",
						ContainerName: "app",
					},
				},
			},
		},
	}
	require.NoError(t, storage.CreateCheckpoint(allocationCheckpointName, &AllocationStateCheckpoint{
		checkpointData: data,
	}))
	chkBefore, err := os.ReadFile(chkPath)
	require.NoError(t, err)
	storage.createAttempts = 0
	storage.removeAttempts = 0
	target := NewAllocationState("test-node-load-claims", topology, cpuset.New(3))

	err = target.LoadState(logger, storage)

	require.NoError(t, err)
	require.Zero(t, storage.createAttempts)
	require.Zero(t, storage.removeAttempts)
	chkAfter, err := os.ReadFile(chkPath)
	require.NoError(t, err)
	require.Equal(t, chkBefore, chkAfter)
	require.Equal(t, "3", target.GetReservedCPUs().String())
	require.Equal(t, AllocationSnapshot{
		AllocatedCPUs:        2,
		AvailableCPUs:        1,
		ReservedCPUs:         1,
		ActiveResourceClaims: 2,
	}, target.Snapshot())
	require.Equal(t, "0-1", target.GetPreparedCPUs().String())
	require.Equal(t, "2", target.GetSharedCPUs().String())

	cpus, found := target.GetResourceClaimAllocation("vacant")
	require.True(t, found)
	require.Equal(t, "0", cpus.String())
	require.False(t, target.IsOwner("vacant", "pod", "app"))

	cpus, found = target.GetResourceClaimAllocation("owned")
	require.True(t, found)
	require.Equal(t, "1", cpus.String())
	require.True(t, target.IsOwner("owned", "pod", "app"))
}

func newAllocationStateForLoadStateTest(nodeName string, cpuTopology *cpuinfo.CPUTopology, reservedCPUs cpuset.CPUSet) (*AllocationState, func(*testing.T, *AllocationState)) {
	cpuIDs := slices.Collect(maps.Keys(cpuTopology.CPUDetails))
	allCPUs := cpuset.New(cpuIDs...)
	wantReserved := reservedCPUs.String()
	wantShared := allCPUs.Difference(reservedCPUs).String()
	wantSnapshot := AllocationSnapshot{
		AvailableCPUs: cpuTopology.NumCPUs - reservedCPUs.Size(),
		ReservedCPUs:  reservedCPUs.Size(),
	}
	target := NewAllocationState(nodeName, cpuTopology, reservedCPUs)
	return target, func(t *testing.T, as *AllocationState) {
		t.Helper()
		require.Equal(t, wantReserved, as.GetReservedCPUs().String())
		require.Equal(t, wantSnapshot, as.Snapshot())
		require.Empty(t, as.GetPreparedCPUs().String())
		require.Equal(t, wantShared, as.GetSharedCPUs().String())
	}
}

func TestAllocationStateLoadStateMissing(t *testing.T) {
	logger := testr.New(t)
	topology := newLoadStateTestTopology()
	storage := newRecordingCheckpointManager(t)
	chkPath := filepath.Join(storage.CheckpointPath(), allocationCheckpointName)
	_, err := os.Stat(chkPath)
	require.ErrorIs(t, err, os.ErrNotExist)
	target, verifyPristine := newAllocationStateForLoadStateTest("test-node-load-missing", topology, cpuset.New(3))

	require.NoError(t, target.LoadState(logger, storage))
	require.Zero(t, storage.createAttempts)
	require.Zero(t, storage.removeAttempts)

	_, err = os.Stat(chkPath) // LoadState does not create files
	require.ErrorIs(t, err, os.ErrNotExist)

	verifyPristine(t, target)
}

func TestAllocationStateLoadStateReadFailure(t *testing.T) {
	logger := testr.New(t)
	topology := newLoadStateTestTopology()
	storage := newRecordingCheckpointManager(t)
	chkPath := filepath.Join(storage.CheckpointPath(), allocationCheckpointName)
	data := allocationStateCheckpointData{
		Meta: allocationStateCheckpointMeta{
			Version:             1,
			NodeName:            "read-fail",
			ReservedCPUs:        "3",
			TopologyFingerprint: MakeTopologyFingerprint(*topology),
		},
		Payload: allocationStateCheckpointPayload{
			ClaimInfo: map[string]claimAllocationCheckpoint{},
		},
	}
	require.NoError(t, storage.CreateCheckpoint(allocationCheckpointName, &AllocationStateCheckpoint{
		checkpointData: data,
	}))
	chkBefore, err := os.ReadFile(chkPath)
	require.NoError(t, err)

	storage.createAttempts = 0
	readErr := errors.New("injected checkpoint read failure")
	storage.Manager = failingCheckpointManager{
		err: fmt.Errorf("read allocation checkpoint: %w", readErr),
	}
	target, verifyPristine := newAllocationStateForLoadStateTest("read-fail", topology, cpuset.New(3))

	err = target.LoadState(logger, storage)
	require.ErrorIs(t, err, readErr)

	// LoadState must do no filesystem changes (read-only)
	require.Zero(t, storage.createAttempts)
	require.Zero(t, storage.removeAttempts)

	// read error = no mutations on the in-memory state
	chkAfter, err := os.ReadFile(chkPath)
	require.NoError(t, err)
	require.Equal(t, chkBefore, chkAfter)

	verifyPristine(t, target)
}

func TestAllocationStateLoadStateDataCorruption(t *testing.T) {
	logger := testr.New(t)
	topology := newLoadStateTestTopology()
	storage := newRecordingCheckpointManager(t)

	chkPath := filepath.Join(storage.CheckpointPath(), allocationCheckpointName)
	// write garbage instead of CBOR-encoded content
	garbage := []byte{0xff}
	require.NoError(t, os.WriteFile(chkPath, garbage, 0600))
	target, verifyPristine := newAllocationStateForLoadStateTest("test-node-load-corrupt", topology, cpuset.New(3))

	err := target.LoadState(logger, storage)
	require.ErrorContains(t, err, "failed to deserialize allocation state checkpoint:")

	// LoadState must do no filesystem changes (read-only)
	require.Zero(t, storage.createAttempts)
	require.Zero(t, storage.removeAttempts)

	// LoadState must not mutate the filesystem checkpoint, even on read error.
	garbageReadBack, err := os.ReadFile(chkPath)
	require.NoError(t, err)
	require.Equal(t, garbage, garbageReadBack)

	verifyPristine(t, target)
}

func TestAllocationStateLoadStateGarbageContent(t *testing.T) {
	logger := testr.New(t)
	topology := newLoadStateTestTopology()
	storage := newRecordingCheckpointManager(t)
	chkPath := filepath.Join(storage.CheckpointPath(), allocationCheckpointName)

	// TestAllocationStateLoadStateDataCorruption wrote pure garbage
	// here we CBOR-encode correctly some garbage.
	garbage := []byte{0xff}
	checksum := checkpoint.NewChecksum(garbage)
	chkBefore, err := cbor.Marshal(&AllocationStateCheckpoint{
		Data:         garbage,
		DataChecksum: checksum,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(chkPath, chkBefore, 0600))
	target, verifyPristine := newAllocationStateForLoadStateTest("test-node-load-corrupt-encoded", topology, cpuset.New(3))

	err = target.LoadState(logger, storage)
	require.ErrorContains(t, err, "failed to deserialize allocation state checkpoint data:")

	// LoadState must do no filesystem changes (read-only)
	require.Zero(t, storage.createAttempts)
	require.Zero(t, storage.removeAttempts)

	// LoadState must not mutate the filesystem checkpoint, even on read error.
	chkAfter, err := os.ReadFile(chkPath)
	require.NoError(t, err)
	require.Equal(t, chkBefore, chkAfter)

	verifyPristine(t, target)
}

func TestAllocationStateLoadStateChecksum(t *testing.T) {
	logger := testr.New(t)
	topology := newLoadStateTestTopology()
	storage := newRecordingCheckpointManager(t)
	chkPath := filepath.Join(storage.CheckpointPath(), allocationCheckpointName)
	data := allocationStateCheckpointData{
		Meta: allocationStateCheckpointMeta{
			Version:             1,
			NodeName:            "load-node",
			ReservedCPUs:        "3",
			TopologyFingerprint: MakeTopologyFingerprint(*topology),
		},
		Payload: allocationStateCheckpointPayload{
			ClaimInfo: map[string]claimAllocationCheckpoint{},
		},
	}
	nested, err := cbor.Marshal(data)
	require.NoError(t, err)
	// now tamper the checksum
	checksum := checkpoint.NewChecksum(nested)
	checksum ^= 1
	before, err := cbor.Marshal(&AllocationStateCheckpoint{
		Data:         nested,
		DataChecksum: checksum,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(chkPath, before, 0600))

	target, verifyPristine := newAllocationStateForLoadStateTest("load-node", topology, cpuset.New(3))
	err = target.LoadState(logger, storage)
	var corruptErr *checkpoint.CorruptCheckpointError
	require.ErrorAs(t, err, &corruptErr)

	// LoadState must do no filesystem changes (read-only)
	require.Zero(t, storage.createAttempts)
	require.Zero(t, storage.removeAttempts)

	after, err := os.ReadFile(chkPath)
	require.NoError(t, err)
	require.Equal(t, before, after)

	verifyPristine(t, target)
}

func TestAllocationStateMutationsPreserveCheckpointMetadata(t *testing.T) {
	logger := testr.New(t)
	topology := newLoadStateTestTopology()
	storage := newRecordingCheckpointManager(t)
	state := NewAllocationState("metadata-node", topology, cpuset.New(3))
	claim := ClaimIdentifier{
		UID: "claim",
		Claim: types.NamespacedName{
			Namespace: "workloads",
			Name:      "cpu-claim",
		},
	}
	wantMeta := allocationStateCheckpointMeta{
		Version:             1,
		NodeName:            "metadata-node",
		ReservedCPUs:        "3",
		TopologyFingerprint: MakeTopologyFingerprint(*topology),
	}

	for _, tc := range []struct {
		name      string
		mutate    func() error
		wantOwner ownerIdentCheckpoint
	}{
		{
			name: "reserve",
			mutate: func() error {
				return state.Reserve(logger, storage, claim, cpuset.New(0), false)
			},
		},
		{
			name: "bind",
			mutate: func() error {
				_, err := state.Bind(logger, storage, "pod", "app", map[types.UID]cpuset.CPUSet{
					"claim": cpuset.New(0),
				})
				return err
			},
			wantOwner: ownerIdentCheckpoint{
				PodUID:        "pod",
				ContainerName: "app",
			},
		},
		{
			name: "unbind",
			mutate: func() error {
				return state.Unbind(logger, storage, "claim")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writes := storage.createAttempts
			require.NoError(t, tc.mutate())
			require.Equal(t, writes+1, storage.createAttempts)
			data := readAllocationCheckpoint(t, storage)
			require.Equal(t, wantMeta, data.Meta)
			require.Equal(t, map[string]claimAllocationCheckpoint{
				"claim": {
					Claim: namespacedNameCheckpoint{
						Namespace: "workloads",
						Name:      "cpu-claim",
					},
					CPUs:  "0",
					Owner: tc.wantOwner,
				},
			}, data.Payload.ClaimInfo)
		})
	}
}

func TestAllocationStateLoadStateReservedCPUsChanged(t *testing.T) {
	for _, tc := range []struct {
		name       string
		previous   cpuset.CPUSet
		current    cpuset.CPUSet
		wantShared string
		wantErr    bool
	}{
		{
			name:       "shrink",
			previous:   cpuset.New(2, 3),
			current:    cpuset.New(3),
			wantShared: "2",
		},
		{
			name:       "grow",
			previous:   cpuset.New(),
			current:    cpuset.New(3),
			wantShared: "2",
		},
		{
			name:       "replace",
			previous:   cpuset.New(3),
			current:    cpuset.New(2),
			wantShared: "3",
		},
		{
			name:     "overlap unbound claim",
			previous: cpuset.New(3),
			current:  cpuset.New(0, 3),
			wantErr:  true,
		},
		{
			name:     "overlap bound claim",
			previous: cpuset.New(3),
			current:  cpuset.New(1, 3),
			wantErr:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logger := testr.New(t)
			manager := newRecordingCheckpointManager(t)
			topology := newLoadStateTestTopology()
			source := NewAllocationState("node", topology, tc.previous)
			require.NoError(t, source.Reserve(logger, manager, ClaimIdentifier{UID: "vacant"}, cpuset.New(0), false))
			require.NoError(t, source.Reserve(logger, manager, ClaimIdentifier{UID: "owned"}, cpuset.New(1), false))
			_, err := source.Bind(logger, manager, "pod", "app", map[types.UID]cpuset.CPUSet{"owned": cpuset.New(1)})
			require.NoError(t, err)
			checkpointBefore := readAllocationCheckpoint(t, manager)
			writes := manager.createAttempts

			target := NewAllocationState("node", topology, tc.current)
			snapshotBefore := target.Snapshot()
			sharedBefore := target.GetSharedCPUs().String()
			err = target.LoadState(logger, manager)
			if tc.wantErr {
				require.ErrorContains(t, err, "CPUs outside the available set")
				require.Equal(t, snapshotBefore, target.Snapshot())
				require.Empty(t, target.GetPreparedCPUs().String())
				require.Equal(t, sharedBefore, target.GetSharedCPUs().String())
				_, found := target.GetResourceClaimAllocation(types.UID("vacant"))
				require.False(t, found)
				_, found = target.GetResourceClaimAllocation(types.UID("owned"))
				require.False(t, found)
			} else {
				require.NoError(t, err)
				require.Equal(t, "0-1", target.GetPreparedCPUs().String())
				require.Equal(t, tc.wantShared, target.GetSharedCPUs().String())
				require.True(t, target.IsOwner("owned", "pod", "app"))
				require.False(t, target.IsOwner("vacant", "pod", "app"))
			}
			require.Equal(t, tc.current.String(), target.GetReservedCPUs().String())
			require.Equal(t, writes, manager.createAttempts)
			require.Equal(t, checkpointBefore, readAllocationCheckpoint(t, manager))
		})
	}
}
