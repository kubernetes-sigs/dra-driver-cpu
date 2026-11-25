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
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/containerd/nri/pkg/api"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/utils/cpuset"
)

// fakeRuntime records the updates the driver pushes and tracks each container's
// cpuset, applying requests in the order they arrive.
type fakeRuntime struct {
	mu     sync.Mutex
	cpus   map[string]cpuset.CPUSet
	calls  [][]*api.ContainerUpdate
	failed []*api.ContainerUpdate
	err    error
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{cpus: map[string]cpuset.CPUSet{}}
}

func (r *fakeRuntime) UpdateContainers(updates []*api.ContainerUpdate) ([]*api.ContainerUpdate, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, updates)
	if r.err != nil {
		return r.failed, r.err
	}
	refused := map[string]bool{}
	for _, u := range r.failed {
		refused[u.GetContainerId()] = true
	}
	for _, u := range updates {
		if !refused[u.GetContainerId()] {
			r.applyLocked([]*api.ContainerUpdate{u})
		}
	}
	return r.failed, nil
}

// apply does what the runtime does with a hook's adjustment and updates.
func (r *fakeRuntime) apply(id string, adjust *api.ContainerAdjustment, updates []*api.ContainerUpdate) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if adjust != nil {
		r.cpus[id] = mustParseCPUSet(adjust.GetLinux().GetResources().GetCpu().GetCpus())
	}
	r.applyLocked(updates)
}

func (r *fakeRuntime) applyLocked(updates []*api.ContainerUpdate) {
	for _, u := range updates {
		r.cpus[u.GetContainerId()] = mustParseCPUSet(u.GetLinux().GetResources().GetCpu().GetCpus())
	}
}

func (r *fakeRuntime) get(id string) cpuset.CPUSet {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cpus[id]
}

func (r *fakeRuntime) allCalls() [][]*api.ContainerUpdate {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([][]*api.ContainerUpdate{}, r.calls...)
}

func mustParseCPUSet(s string) cpuset.CPUSet {
	parsed, err := cpuset.Parse(s)
	if err != nil {
		panic(err)
	}
	return parsed
}

// reconcileFixture: 4-CPU driver, one exclusive claim on 0-1 whose container
// has stopped, two shared containers on 2-3. The state right before an
// Unprepare.
type reconcileFixture struct {
	t       *testing.T
	d       *CPUDriver
	runtime *fakeRuntime

	exclusiveCPUs cpuset.CPUSet
	sharedIDs     []string
	oldClaim      *resourceapi.ResourceClaim
	newClaim      *resourceapi.ResourceClaim
}

func newReconcileFixture(t *testing.T, runtime *fakeRuntime) *reconcileFixture {
	t.Helper()
	d, _ := newMetricsTestDriverWithCPUInfos(t, mockCPUInfos_SingleSocket_4CPUS_HT)
	d.reconcile = &reconcileState{containerUpdater: runtime}
	_, err := d.Synchronize(t.Context(), nil, nil)
	require.NoError(t, err)

	f := &reconcileFixture{
		t:             t,
		d:             d,
		runtime:       runtime,
		exclusiveCPUs: cpuset.New(0, 1),
		sharedIDs:     []string{"shared-id-1", "shared-id-2"},
		oldClaim:      individualMetricsClaim("claim-old", "cpudev0", "cpudev1"),
		newClaim:      individualMetricsClaim("claim-new", "cpudev0", "cpudev1"),
	}

	f.prepare(f.oldClaim)
	pod, ctr := f.exclusiveContainer("old", f.oldClaim)
	adjust, updates, err := d.CreateContainer(t.Context(), pod, ctr)
	require.NoError(t, err)
	runtime.apply(ctr.Id, adjust, updates)
	for i, id := range f.sharedIDs {
		sharedPod := &api.PodSandbox{Id: fmt.Sprintf("shared-sandbox-%d", i+1), Uid: fmt.Sprintf("shared-pod-%d", i+1)}
		sharedCtr := &api.Container{Id: id, Name: "shared", PodSandboxId: sharedPod.Id}
		adjust, updates, err := d.CreateContainer(t.Context(), sharedPod, sharedCtr)
		require.NoError(t, err)
		runtime.apply(id, adjust, updates)
		require.Equal(t, "2-3", runtime.get(id).String())
	}
	updates, err = d.StopContainer(t.Context(), pod, ctr)
	require.NoError(t, err)
	runtime.apply("", nil, updates)

	return f
}

func (f *reconcileFixture) exclusiveContainer(suffix string, claim *resourceapi.ResourceClaim) (*api.PodSandbox, *api.Container) {
	pod := &api.PodSandbox{Id: "exclusive-sandbox-" + suffix, Uid: "exclusive-pod-" + suffix}
	ctr := &api.Container{
		Id:           "exclusive-id-" + suffix,
		Name:         "exclusive",
		PodSandboxId: pod.Id,
		Env:          []string{fmt.Sprintf("%s_%s=%s", cdiEnvVarPrefix, claim.UID, f.exclusiveCPUs.String())},
	}
	return pod, ctr
}

func (f *reconcileFixture) prepare(claim *resourceapi.ResourceClaim) {
	f.t.Helper()
	results, err := f.d.PrepareResourceClaims(f.t.Context(), []*resourceapi.ResourceClaim{claim})
	require.NoError(f.t, err)
	require.NoError(f.t, results[claim.UID].Err)
}

func (f *reconcileFixture) unprepare(claim *resourceapi.ResourceClaim) {
	f.t.Helper()
	results, err := f.d.UnprepareResourceClaims(f.t.Context(), []kubeletplugin.NamespacedObject{{UID: claim.UID}})
	require.NoError(f.t, err)
	require.NoError(f.t, results[claim.UID])
}

func (f *reconcileFixture) createExclusive(suffix string, claim *resourceapi.ResourceClaim) string {
	f.t.Helper()
	pod, ctr := f.exclusiveContainer(suffix, claim)
	adjust, updates, err := f.d.CreateContainer(f.t.Context(), pod, ctr)
	require.NoError(f.t, err)
	require.Equal(f.t, f.exclusiveCPUs.String(), adjust.GetLinux().GetResources().GetCpu().GetCpus())
	require.Len(f.t, updates, len(f.sharedIDs), "a new owner must move every shared container off its CPUs")
	f.runtime.apply(ctr.Id, adjust, updates)
	return ctr.Id
}

func (f *reconcileFixture) requireSharedCPUs(want string) {
	f.t.Helper()
	for _, id := range f.sharedIDs {
		require.Equal(f.t, want, f.runtime.get(id).String(), "shared container %s", id)
	}
}

func (f *reconcileFixture) requireNoOverlap(exclusiveID string) {
	f.t.Helper()
	exclusive := f.runtime.get(exclusiveID)
	require.Equal(f.t, f.exclusiveCPUs.String(), exclusive.String())
	for _, id := range f.sharedIDs {
		shared := f.runtime.get(id)
		require.Truef(f.t, shared.Intersection(exclusive).IsEmpty(),
			"shared container %s runs on %s, overlapping exclusive container %s on %s",
			id, shared, exclusiveID, exclusive)
	}
}

func TestReconcileWidensSharedContainersAfterUnprepare(t *testing.T) {
	f := newReconcileFixture(t, newFakeRuntime())
	f.requireSharedCPUs("2-3")

	f.unprepare(f.oldClaim)

	require.Equal(t, "0-3", f.runtime.get(f.sharedIDs[0]).String(), "Unprepare must widen the shared containers")
	require.Equal(t, "0-3", f.runtime.get(f.sharedIDs[1]).String())
	calls := f.runtime.allCalls()
	require.Len(t, calls, 1, "one release must cost one update")
	require.Len(t, calls[0], len(f.sharedIDs))
}

// Unprepare widens before it returns, and kubeletplugin serializes Unprepare
// against Prepare, so the widening always lands before the next claim can take
// those CPUs back. There is no window to reorder, which is why a subsequent
// Prepare plus CreateContainer simply shrinks the pool again.
func TestReconcileWideningPrecedesNextPrepare(t *testing.T) {
	f := newReconcileFixture(t, newFakeRuntime())

	f.unprepare(f.oldClaim)
	f.requireSharedCPUs("0-3")

	f.prepare(f.newClaim)
	exclusiveID := f.createExclusive("new", f.newClaim)
	f.requireSharedCPUs("2-3")
	f.requireNoOverlap(exclusiveID)
}

func TestReconcileToleratesFailedUpdate(t *testing.T) {
	runtime := newFakeRuntime()
	f := newReconcileFixture(t, runtime)

	// A refused update is today's behavior, so not fatal: the allocation is
	// already released by then, so Unprepare must not fail on it.
	runtime.err = errors.New("runtime refused")
	f.unprepare(f.oldClaim)
	require.Len(t, f.runtime.allCalls(), 1)
	f.requireSharedCPUs("2-3")
	f.prepare(f.newClaim)

	// A partially refused update must not be rolled back or retried either:
	// the refused container stays where it was, the other one is widened.
	runtime.err = nil
	runtime.failed = []*api.ContainerUpdate{{ContainerId: f.sharedIDs[0]}}
	f.unprepare(f.newClaim)
	require.Len(t, f.runtime.allCalls(), 2)
	require.Equal(t, "2-3", f.runtime.get(f.sharedIDs[0]).String(), "refused container must keep its cpuset")
	require.Equal(t, "0-3", f.runtime.get(f.sharedIDs[1]).String())
	f.prepare(f.newClaim)
}

func TestReconcileNoOpWithoutUpdater(t *testing.T) {
	f := newReconcileFixture(t, newFakeRuntime())
	f.d.reconcile.containerUpdater = nil

	f.unprepare(f.oldClaim)
	f.prepare(f.newClaim)
	require.Empty(t, f.runtime.allCalls())
}

func TestRequestReconcileIsInertWhenDisabled(t *testing.T) {
	d, _ := newMetricsTestDriverWithCPUInfos(t, mockCPUInfos_SingleSocket_4CPUS_HT)
	_, err := d.Synchronize(t.Context(), nil, nil)
	require.NoError(t, err)
	claim := individualMetricsClaim("claim", "cpudev0")
	results, err := d.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.NoError(t, results[claim.UID].Err)

	// Feature off: no updater, no update, Unprepare still has to succeed.
	unprepared, err := d.UnprepareResourceClaims(t.Context(), []kubeletplugin.NamespacedObject{{UID: claim.UID}})
	require.NoError(t, err)
	require.NoError(t, unprepared[claim.UID])
	_, found := d.allocationState.GetResourceClaimAllocation(claim.UID)
	require.False(t, found)
}

func TestConfigureDecidesSharedCPUReclaim(t *testing.T) {
	on, off := true, false
	tests := []struct {
		name        string
		setting     *bool
		runtime     string
		version     string
		wantEnabled bool
	}{
		{name: "unset, safe containerd", runtime: "containerd", version: "2.4.0", wantEnabled: true},
		{name: "unset, safe cri-o", runtime: "cri-o", version: "1.37.0", wantEnabled: true},
		{name: "unset, old containerd", runtime: "containerd", version: "2.3.7"},
		{name: "unset, unknown runtime", runtime: "k3s", version: "1.32.1+k3s1"},
		{name: "unset, no runtime info"},
		{name: "forced off, safe runtime", setting: &off, runtime: "containerd", version: "2.4.0"},
		{name: "forced on, old runtime", setting: &on, runtime: "containerd", version: "2.3.7", wantEnabled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, _ := newMetricsTestDriverWithCPUInfos(t, mockCPUInfos_SingleSocket_4CPUS_HT)
			d.forceImmediateSharedCPUReclaim = tt.setting
			d.reconcile = nil
			if ptrBool(tt.setting) {
				d.reconcile = &reconcileState{}
			}

			_, err := d.Configure(t.Context(), "", tt.runtime, tt.version)
			require.NoError(t, err)
			require.Equal(t, tt.wantEnabled, d.reconcile != nil)
		})
	}
}

// A reconnect to an older runtime must turn auto-detected reclaim off again,
// and an Unprepare after that must not reach for the old updater.
func TestConfigureReconnectToOldRuntimeDisablesReclaim(t *testing.T) {
	f := newReconcileFixture(t, newFakeRuntime())
	_, err := f.d.Configure(t.Context(), "", "containerd", "2.3.7")
	require.NoError(t, err)
	require.Nil(t, f.d.reconcile)

	f.unprepare(f.oldClaim)
	require.Empty(t, f.runtime.allCalls())
	f.requireSharedCPUs("2-3")
}

func ptrBool(b *bool) bool { return b != nil && *b }
