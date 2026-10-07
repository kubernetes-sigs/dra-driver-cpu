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
	"fmt"
	"sync"
	"testing"

	"github.com/containerd/nri/pkg/api"
	"github.com/go-logr/logr/testr"
	cpumetrics "github.com/kubernetes-sigs/dra-driver-cpu/pkg/metrics"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/utils/cpuset"
)

type allocationRecorder struct {
	Recorder
	state cpumetrics.AllocationState
}

func (r *allocationRecorder) SetAllocationState(state cpumetrics.AllocationState) { r.state = state }

func TestSynchronizeFailurePreservesState(t *testing.T) {
	d, _ := newMetricsTestDriver(t)
	recorder := &allocationRecorder{Recorder: cpumetrics.Noop()}
	d.metrics = recorder
	d.refreshAllocationMetrics()
	oldMetrics := recorder.state
	oldCPU, oldPods, oldOwners := d.cpuAllocationStore, d.podConfigStore, d.claimTracker
	d.cdiMgr = newMockCdiMgrWithAllocations(map[types.UID]cpuset.CPUSet{"full": cpuset.New(0, 1, 2, 3)})
	pod := &api.PodSandbox{Id: "sandbox", Uid: "pod"}
	updates, err := d.Synchronize(t.Context(), []*api.PodSandbox{pod}, []*api.Container{
		{Id: "exclusive", Name: "app", PodSandboxId: pod.Id, Env: []string{"DRA_CPUSET_full=0-3"}},
		{Id: "shared", Name: "sidecar", PodSandboxId: pod.Id},
	})
	require.ErrorContains(t, err, "no shared CPUs available")
	require.Nil(t, updates)
	require.Same(t, oldCPU, d.cpuAllocationStore)
	require.Same(t, oldPods, d.podConfigStore)
	require.Same(t, oldOwners, d.claimTracker)
	require.Equal(t, oldMetrics, recorder.state)
	require.True(t, d.cpuAllocationStore.GetPreparedCPUs().IsEmpty())
	require.Zero(t, d.claimTracker.Len())
	require.False(t, d.hasSynchronized)
}

func TestSynchronizeRetainsPreparedClaimAndOwner(t *testing.T) {
	d, _ := newMetricsTestDriver(t)
	_, err := d.Synchronize(t.Context(), nil, nil)
	require.NoError(t, err)
	claim := individualMetricsClaim("pending", "cpudev0")
	prepared, err := d.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.NoError(t, prepared[claim.UID].Err)
	pod := &api.PodSandbox{Id: "sandbox", Uid: "pod"}
	app := &api.Container{Id: "old", Name: "app", PodSandboxId: pod.Id, Env: []string{"DRA_CPUSET_pending=0"}}
	_, _, err = d.CreateContainer(t.Context(), pod, app)
	require.NoError(t, err)
	_, err = d.StopContainer(t.Context(), pod, app)
	require.NoError(t, err)
	// The old shared-container ID must also be discarded by recovery.
	d.podConfigStore.SetContainerState("old-pod", store.NewContainerState("old-shared", "stale"))
	updates, err := d.Synchronize(t.Context(), []*api.PodSandbox{pod}, []*api.Container{
		{Id: "shared", Name: "sidecar", PodSandboxId: pod.Id},
	})
	require.NoError(t, err)
	require.Len(t, updates, 1)
	require.Equal(t, "shared", updates[0].ContainerId)
	require.Equal(t, "1-3", updates[0].Linux.Resources.Cpu.Cpus)
	cpus, found := d.cpuAllocationStore.GetResourceClaimAllocation(claim.UID)
	require.True(t, found)
	require.Equal(t, "0", cpus.String())
	require.Equal(t, 1, d.claimTracker.Len())
	require.Nil(t, d.podConfigStore.GetContainerState("old-pod", "old-shared"))
	// Ownership survives an empty snapshot as well.
	_, err = d.Synchronize(t.Context(), nil, nil)
	require.NoError(t, err)
	_, _, err = d.CreateContainer(t.Context(), &api.PodSandbox{Uid: "other"}, app)
	require.Error(t, err)
	app.Id = "replacement"
	_, _, err = d.CreateContainer(t.Context(), pod, app)
	require.NoError(t, err)
}

func TestWarmSynchronizeRejectsConflictingClaims(t *testing.T) {
	for _, scenario := range []string{"missing CDI", "changed CPU", "different owner", "unknown authenticated claim"} {
		t.Run(scenario, func(t *testing.T) {
			d, _ := newMetricsTestDriver(t)
			_, err := d.Synchronize(t.Context(), nil, nil)
			require.NoError(t, err)
			cdi := newMockCdiMgrWithAllocations(map[types.UID]cpuset.CPUSet{"a": cpuset.New(0)})
			d.cdiMgr = cdi
			requirePreparedResourceClaim(t, testr.New(t), d.cpuAllocationStore, "a", cpuset.New(0))
			_, err = d.claimTracker.SetOwner(testr.New(t), "pod", "app", "a")
			require.NoError(t, err)
			pod := &api.PodSandbox{Id: "sandbox", Uid: "pod"}
			app := &api.Container{Id: "app", Name: "app", PodSandboxId: pod.Id, Env: []string{"DRA_CPUSET_a=0"}}
			switch scenario {
			case "missing CDI":
				delete(cdi.devices, getCDIDeviceName("a"))
			case "changed CPU":
				app.Env = []string{"DRA_CPUSET_a=1"}
			case "different owner":
				app.Name = "other"
			case "unknown authenticated claim":
				cdi.devices[getCDIDeviceName("b")] = "DRA_CPUSET_b=1"
				app.Env = []string{"DRA_CPUSET_b=1"}
			}
			oldCPU, oldPods, oldOwners := d.cpuAllocationStore, d.podConfigStore, d.claimTracker
			_, err = d.Synchronize(t.Context(), []*api.PodSandbox{pod}, []*api.Container{app})
			require.Error(t, err)
			require.Same(t, oldCPU, d.cpuAllocationStore)
			require.Same(t, oldPods, d.podConfigStore)
			require.Same(t, oldOwners, d.claimTracker)
			require.Equal(t, "0", d.cpuAllocationStore.GetPreparedCPUs().String())
		})
	}
}

func TestWarmSynchronizeDoesNotResurrectUnpreparedClaim(t *testing.T) {
	d, _ := newMetricsTestDriver(t)
	_, err := d.Synchronize(t.Context(), nil, nil)
	require.NoError(t, err)
	claim := individualMetricsClaim("a", "cpudev0")
	results, err := d.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.NoError(t, results[claim.UID].Err)
	pod := &api.PodSandbox{Id: "sandbox", Uid: "pod"}
	ctr := &api.Container{Id: "app", Name: "app", PodSandboxId: pod.Id, Env: []string{"DRA_CPUSET_a=0"}}
	_, _, err = d.CreateContainer(t.Context(), pod, ctr)
	require.NoError(t, err)
	_, err = d.StopContainer(t.Context(), pod, ctr)
	require.NoError(t, err)
	unprepared, err := d.UnprepareResourceClaims(t.Context(), []kubeletplugin.NamespacedObject{{UID: claim.UID}})
	require.NoError(t, err)
	require.NoError(t, unprepared[claim.UID])
	_, err = d.Synchronize(t.Context(), []*api.PodSandbox{pod}, []*api.Container{ctr})
	require.NoError(t, err)
	require.True(t, d.cpuAllocationStore.GetPreparedCPUs().IsEmpty())
	require.Zero(t, d.claimTracker.Len())
	// Even stale CDI cannot override an authoritative in-process release.
	d.cdiMgr.(*mockCdiMgr).devices[getCDIDeviceName("a")] = "DRA_CPUSET_a=0"
	_, err = d.Synchronize(t.Context(), []*api.PodSandbox{pod}, []*api.Container{ctr})
	require.ErrorContains(t, err, "without a prepared allocation")
	require.True(t, d.cpuAllocationStore.GetPreparedCPUs().IsEmpty())
}

type pausedRefreshCDI struct {
	*mockCdiMgr
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (m *pausedRefreshCDI) Refresh() error {
	m.once.Do(func() { close(m.entered); <-m.release })
	return m.mockCdiMgr.Refresh()
}

func TestSynchronizeSerializesDRAOperations(t *testing.T) {
	for _, operation := range []string{"prepare", "unprepare", "cancelled prepare"} {
		t.Run(operation, func(t *testing.T) {
			d, _ := newMetricsTestDriver(t)
			claim := individualMetricsClaim("pending", "cpudev0")
			cdi := &pausedRefreshCDI{mockCdiMgr: newMockCdiMgrWithAllocations(map[types.UID]cpuset.CPUSet{"running": cpuset.New(1)}), entered: make(chan struct{}), release: make(chan struct{})}
			d.cdiMgr = cdi
			if operation == "unprepare" {
				results, err := d.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
				require.NoError(t, err)
				require.NoError(t, results[claim.UID].Err)
			}
			pod := &api.PodSandbox{Id: "sandbox", Uid: "pod"}
			syncDone := make(chan error, 1)
			go func() {
				_, err := d.Synchronize(t.Context(), []*api.PodSandbox{pod}, []*api.Container{{Id: "app", Name: "app", PodSandboxId: pod.Id, Env: []string{"DRA_CPUSET_running=1"}}})
				syncDone <- err
			}()
			<-cdi.entered
			// Verify that the state lock covers CDI I/O, not only the final swap.
			locked := !d.stateMu.TryLock()
			if !locked {
				d.stateMu.Unlock()
			}
			require.True(t, locked)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			started := make(chan struct{})
			go func() {
				close(started)
				if operation == "unprepare" {
					results, err := d.UnprepareResourceClaims(ctx, []kubeletplugin.NamespacedObject{{UID: claim.UID}})
					if err == nil {
						err = results[claim.UID]
					}
					done <- err
				} else {
					results, err := d.PrepareResourceClaims(ctx, []*resourceapi.ResourceClaim{claim})
					if err == nil {
						err = results[claim.UID].Err
					}
					done <- err
				}
			}()
			<-started
			if operation == "cancelled prepare" {
				cancel()
			}
			close(cdi.release)
			require.NoError(t, <-syncDone)
			err := <-done
			if operation == "cancelled prepare" {
				require.ErrorIs(t, err, context.Canceled)
			} else {
				require.NoError(t, err)
			}
			cpus, found := d.cpuAllocationStore.GetResourceClaimAllocation(claim.UID)
			if operation == "prepare" {
				require.True(t, found)
				require.Equal(t, "0", cpus.String())
				require.Equal(t, "DRA_CPUSET_pending=0", cdi.devices[getCDIDeviceName(claim.UID)])
			} else {
				require.False(t, found)
			}
		})
	}
}

func TestSynchronizeCancelledBeforeCommit(t *testing.T) {
	d, _ := newMetricsTestDriver(t)
	cdi := &pausedRefreshCDI{mockCdiMgr: newMockCdiMgrWithAllocations(map[types.UID]cpuset.CPUSet{"a": cpuset.New(0)}), entered: make(chan struct{}), release: make(chan struct{})}
	d.cdiMgr = cdi
	old := d.cpuAllocationStore
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := d.Synchronize(ctx, []*api.PodSandbox{{Id: "sandbox", Uid: "pod"}}, []*api.Container{{Id: "app", Name: "app", PodSandboxId: "sandbox", Env: []string{fmt.Sprintf("%s_a=0", cdiEnvVarPrefix)}}})
		done <- err
	}()
	<-cdi.entered
	cancel()
	close(cdi.release)
	require.ErrorIs(t, <-done, context.Canceled)
	require.Same(t, old, d.cpuAllocationStore)
	require.False(t, d.hasSynchronized)
}

func TestWarmSynchronizeIgnoresInvalidEnvFromNonOwner(t *testing.T) {
	for _, missingCDI := range []bool{false, true} {
		t.Run(fmt.Sprintf("missingCDI=%v", missingCDI), func(t *testing.T) {
			d, _ := newMetricsTestDriver(t)
			_, err := d.Synchronize(t.Context(), nil, nil)
			require.NoError(t, err)
			claim := individualMetricsClaim("a", "cpudev0")
			results, err := d.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
			require.NoError(t, err)
			require.NoError(t, results[claim.UID].Err)
			_, err = d.claimTracker.SetOwner(testr.New(t), "real-pod", "app", claim.UID)
			require.NoError(t, err)
			if missingCDI {
				delete(d.cdiMgr.(*mockCdiMgr).devices, getCDIDeviceName(claim.UID))
			}
			pod := &api.PodSandbox{Id: "sandbox", Uid: "other-pod"}
			updates, err := d.Synchronize(t.Context(), []*api.PodSandbox{pod}, []*api.Container{{Id: "other", Name: "app", PodSandboxId: pod.Id, Env: []string{"DRA_CPUSET_a=1"}}})
			require.NoError(t, err, "an untrusted env must not break node-wide recovery")
			require.Equal(t, "0", d.cpuAllocationStore.GetPreparedCPUs().String())
			require.True(t, d.claimTracker.IsOwner(claim.UID, "real-pod", "app"))
			require.Len(t, updates, 1)
			require.Equal(t, "1-3", updates[0].Linux.Resources.Cpu.Cpus)
		})
	}
}

func TestWarmSynchronizeRetainsMalformedSharedContainer(t *testing.T) {
	d, _ := newMetricsTestDriver(t)
	_, err := d.Synchronize(t.Context(), nil, nil)
	require.NoError(t, err)
	pod := &api.PodSandbox{Id: "sandbox", Uid: "pod"}
	shared := &api.Container{Id: "shared", Name: "sidecar", PodSandboxId: pod.Id}
	_, _, err = d.CreateContainer(t.Context(), pod, shared)
	require.NoError(t, err)
	// Another NRI plugin may change the environment after our CreateContainer.
	shared.Env = []string{"DRA_CPUSET_other=bad"}
	_, err = d.Synchronize(t.Context(), []*api.PodSandbox{pod}, []*api.Container{shared})
	require.NoError(t, err)
	claim := individualMetricsClaim("a", "cpudev0")
	prepared, err := d.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.NoError(t, prepared[claim.UID].Err)
	_, updates, err := d.CreateContainer(t.Context(), pod, &api.Container{
		Id: "exclusive", Name: "app", PodSandboxId: pod.Id, Env: []string{"DRA_CPUSET_a=0"},
	})
	require.NoError(t, err)
	require.Len(t, updates, 1)
	require.Equal(t, "shared", updates[0].ContainerId)
	require.Equal(t, "1-3", updates[0].Linux.Resources.Cpu.Cpus)
}

func TestWarmSynchronizeMalformedContainerIdentity(t *testing.T) {
	for _, tc := range []struct {
		name          string
		podUID        string
		containerName string
		containerID   string
		env           []string
		known         bool
		present       bool
		wantRetain    bool
	}{
		{name: "unknown container", podUID: "pod", containerName: "sidecar", containerID: "shared", env: []string{"DRA_CPUSET_other=bad"}, present: true},
		{name: "same name replacement", podUID: "pod", containerName: "sidecar", containerID: "replacement", env: []string{"DRA_CPUSET_other=bad"}, known: true, present: true},
		{name: "different pod", podUID: "other-pod", containerName: "sidecar", containerID: "shared", env: []string{"DRA_CPUSET_other=bad"}, known: true, present: true},
		{name: "different name", podUID: "pod", containerName: "other-name", containerID: "shared", env: []string{"DRA_CPUSET_other=bad"}, known: true, present: true},
		{name: "absent from snapshot", podUID: "pod", known: true},
		{name: "missing equals", podUID: "pod", containerName: "sidecar", containerID: "shared", env: []string{"DRA_CPUSET_other"}, known: true, present: true, wantRetain: true},
		{name: "malformed before valid claim", podUID: "pod", containerName: "sidecar", containerID: "shared", env: []string{"DRA_CPUSET_other=bad", "DRA_CPUSET_a=0"}, known: true, present: true, wantRetain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, _ := newMetricsTestDriver(t)
			_, err := d.Synchronize(t.Context(), nil, nil)
			require.NoError(t, err)
			pod := &api.PodSandbox{Id: "sandbox", Uid: "pod"}
			if tc.known {
				_, _, err = d.CreateContainer(t.Context(), pod, &api.Container{Id: "shared", Name: "sidecar", PodSandboxId: pod.Id})
				require.NoError(t, err)
			}
			snapshotPod := &api.PodSandbox{Id: pod.Id, Uid: tc.podUID}
			var containers []*api.Container
			if tc.present {
				containers = append(containers, &api.Container{Id: tc.containerID, Name: tc.containerName, PodSandboxId: pod.Id, Env: tc.env})
			}
			// A healthy container must still be recovered alongside malformed input.
			containers = append(containers, &api.Container{Id: "healthy", Name: "healthy", PodSandboxId: pod.Id})
			_, err = d.Synchronize(t.Context(), []*api.PodSandbox{snapshotPod}, containers)
			require.NoError(t, err)
			claim := individualMetricsClaim("a", "cpudev0")
			prepared, err := d.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
			require.NoError(t, err)
			require.NoError(t, prepared[claim.UID].Err)
			_, updates, err := d.CreateContainer(t.Context(), snapshotPod, &api.Container{
				Id: "exclusive", Name: "app", PodSandboxId: pod.Id, Env: []string{"DRA_CPUSET_a=0"},
			})
			require.NoError(t, err)
			got := make(map[string]string)
			for _, update := range updates {
				got[update.ContainerId] = update.Linux.Resources.Cpu.Cpus
			}
			want := map[string]string{"healthy": "1-3"}
			if tc.wantRetain {
				want["shared"] = "1-3"
			}
			require.Equal(t, want, got)
		})
	}
}

func TestWarmSynchronizeMalformedExclusiveContainerKeepsOwnership(t *testing.T) {
	d, _ := newMetricsTestDriver(t)
	_, err := d.Synchronize(t.Context(), nil, nil)
	require.NoError(t, err)
	claim := individualMetricsClaim("a", "cpudev0")
	prepared, err := d.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.NoError(t, prepared[claim.UID].Err)
	pod := &api.PodSandbox{Id: "sandbox", Uid: "pod"}
	app := &api.Container{Id: "app", Name: "app", PodSandboxId: pod.Id, Env: []string{"DRA_CPUSET_a=0"}}
	_, _, err = d.CreateContainer(t.Context(), pod, app)
	require.NoError(t, err)
	// A malformed unrelated entry must not turn an exclusive container into
	// a shared one or make us trust a changed allocation later in the env.
	app.Env = []string{"DRA_CPUSET_other=bad", "DRA_CPUSET_a=1"}
	updates, err := d.Synchronize(t.Context(), []*api.PodSandbox{pod}, []*api.Container{
		app, {Id: "shared", Name: "sidecar", PodSandboxId: pod.Id},
	})
	require.NoError(t, err)
	require.Len(t, updates, 1)
	require.Equal(t, "shared", updates[0].ContainerId)
	require.Equal(t, "1-3", updates[0].Linux.Resources.Cpu.Cpus)
	_, _, err = d.CreateContainer(t.Context(), &api.PodSandbox{Uid: "other"}, &api.Container{
		Id: "other-app", Name: app.Name, Env: []string{"DRA_CPUSET_a=0"},
	})
	require.Error(t, err, "another pod cannot take the retained claim")
	app.Id = "replacement"
	app.Env = []string{"DRA_CPUSET_a=0"}
	adjustment, _, err := d.CreateContainer(t.Context(), pod, app)
	require.NoError(t, err)
	require.Equal(t, "0", adjustment.Linux.Resources.Cpu.Cpus)
}
