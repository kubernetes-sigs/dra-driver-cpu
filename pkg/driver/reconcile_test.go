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
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/containerd/nri/pkg/api"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/utils/cpuset"
)

const reconcileStepTimeout = 2 * time.Second

// fakeRuntime records the updates the driver pushes and tracks each container's
// cpuset, applying requests in the order they arrive. With pause set, an update
// is handed to the test on taken and applied only after release is closed, so
// "worker computed it" and "runtime applied it" become two separate events.
type fakeRuntime struct {
	mu     sync.Mutex
	cpus   map[string]cpuset.CPUSet
	calls  [][]*api.ContainerUpdate
	failed []*api.ContainerUpdate
	err    error

	pause   bool
	taken   chan []*api.ContainerUpdate
	release chan struct{}
}

func newFakeRuntime() *fakeRuntime {
	return &fakeRuntime{cpus: map[string]cpuset.CPUSet{}}
}

func newPausedFakeRuntime() *fakeRuntime {
	r := newFakeRuntime()
	r.pause = true
	r.taken = make(chan []*api.ContainerUpdate, 1)
	r.release = make(chan struct{})
	return r
}

func (r *fakeRuntime) UpdateContainers(updates []*api.ContainerUpdate) ([]*api.ContainerUpdate, error) {
	if r.pause {
		select {
		case r.taken <- updates:
		case <-time.After(reconcileStepTimeout):
			return nil, errors.New("test did not collect the snapshot in time")
		}
		select {
		case <-r.release:
		case <-time.After(reconcileStepTimeout):
			return nil, errors.New("test did not release the snapshot in time")
		}
	}
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

func recvWithin[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(reconcileStepTimeout):
		t.Fatalf("timed out waiting for %s", what)
		var zero T
		return zero
	}
}

// reconcileFixture: 4-CPU driver with the worker running, one exclusive claim
// on 0-1 whose container has stopped, two shared containers on 2-3. The state
// right before an Unprepare.
type reconcileFixture struct {
	t       *testing.T
	d       *CPUDriver
	runtime *fakeRuntime
	stop    func()

	exclusiveCPUs cpuset.CPUSet
	sharedIDs     []string
	oldClaim      *resourceapi.ResourceClaim
	newClaim      *resourceapi.ResourceClaim
}

func newReconcileFixture(t *testing.T, runtime *fakeRuntime) *reconcileFixture {
	t.Helper()
	d, _ := newMetricsTestDriverWithCPUInfos(t, mockCPUInfos_SingleSocket_4CPUS_HT)
	d.containerUpdater = runtime
	d.reconcileTrigger = make(chan struct{}, 1)
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

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		d.runReconcileWorker(ctx)
		close(done)
	}()
	f.stop = func() {
		cancel()
		<-done
	}
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

// takeSnapshot waits for the worker's update to reach the paused runtime.
func (f *reconcileFixture) takeSnapshot() {
	f.t.Helper()
	snapshot := recvWithin(f.t, f.runtime.taken, "worker snapshot after Unprepare")
	require.Len(f.t, snapshot, len(f.sharedIDs))
	for _, u := range snapshot {
		require.Equal(f.t, "0-3", u.GetLinux().GetResources().GetCpu().GetCpus())
	}
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
	defer f.stop()
	f.requireSharedCPUs("2-3")

	f.unprepare(f.oldClaim)

	require.Eventually(t, func() bool {
		return f.runtime.get(f.sharedIDs[0]).String() == "0-3" && f.runtime.get(f.sharedIDs[1]).String() == "0-3"
	}, reconcileStepTimeout, 5*time.Millisecond, "worker did not widen the shared containers")
	calls := f.runtime.allCalls()
	require.Len(t, calls, 1, "one release must cost one update")
	require.Len(t, calls[0], len(f.sharedIDs))
}

// The order this has to survive:
//
//  1. Unprepare releases claim-old; the worker computes 0-3, runtime has not
//     applied it yet.
//  2. Prepare for claim-new asks for 0-1 again.
//  3. CreateContainer for claim-new pins it to 0-1 and shrinks shared back to 2-3.
//  4. The 0-3 from step 1 reaches the runtime.
//
// If Prepare returns at step 2, step 4 puts shared containers back on the new
// exclusive container's CPUs. So Prepare has to wait for step 1 to land, and the
// only thing that bounds the wait is the caller's deadline: a hung update has to
// come out as a Prepare error kubelet retries, not as a reservation under a
// stale snapshot.
//
// Prepare runs on this goroutine with a short deadline, so the test cannot pass
// by the Prepare call being scheduled after the update is released.
func TestReconcilePrepareWaitsForInFlightUpdate(t *testing.T) {
	f := newReconcileFixture(t, newPausedFakeRuntime())
	defer f.stop()

	f.unprepare(f.oldClaim)
	f.takeSnapshot()

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	results, err := f.d.PrepareResourceClaims(ctx, []*resourceapi.ResourceClaim{f.newClaim})
	require.NoError(t, err)
	err = results[f.newClaim.UID].Err
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "shared container update still in flight")
	_, reserved := f.d.allocationState.GetResourceClaimAllocation(f.newClaim.UID)
	require.False(t, reserved, "Prepare must not reserve CPUs under an in-flight update")
	f.requireSharedCPUs("2-3")

	close(f.runtime.release)
	f.prepare(f.newClaim)
	f.requireSharedCPUs("0-3")

	exclusiveID := f.createExclusive("new", f.newClaim)
	f.requireSharedCPUs("2-3")
	f.requireNoOverlap(exclusiveID)
}

// Control: once the widening has landed, CreateContainer's own updates shrink
// the pool again and nothing overlaps.
func TestReconcileWideningBeforeReallocationIsSafe(t *testing.T) {
	f := newReconcileFixture(t, newPausedFakeRuntime())
	defer f.stop()

	f.unprepare(f.oldClaim)
	f.takeSnapshot()
	close(f.runtime.release)
	require.Eventually(t, func() bool {
		return f.runtime.get(f.sharedIDs[0]).String() == "0-3"
	}, reconcileStepTimeout, 5*time.Millisecond)

	f.prepare(f.newClaim)
	exclusiveID := f.createExclusive("new", f.newClaim)
	f.requireNoOverlap(exclusiveID)
}

func TestReconcileToleratesFailedUpdate(t *testing.T) {
	runtime := newFakeRuntime()
	f := newReconcileFixture(t, runtime)
	defer f.stop()

	// A refused update is today's behavior, so not fatal, and must not leave
	// Prepare waiting on a marker that never clears.
	runtime.err = errors.New("runtime refused")
	f.unprepare(f.oldClaim)
	require.Eventually(t, func() bool { return len(f.runtime.allCalls()) == 1 }, reconcileStepTimeout, 5*time.Millisecond)
	f.requireSharedCPUs("2-3")
	f.prepare(f.newClaim)

	// A partially refused update must not be rolled back or retried either:
	// the refused container stays where it was, the other one is widened.
	runtime.err = nil
	runtime.failed = []*api.ContainerUpdate{{ContainerId: f.sharedIDs[0]}}
	f.unprepare(f.newClaim)
	require.Eventually(t, func() bool { return len(f.runtime.allCalls()) == 2 }, reconcileStepTimeout, 5*time.Millisecond)
	require.Equal(t, "2-3", f.runtime.get(f.sharedIDs[0]).String(), "refused container must keep its cpuset")
	require.Equal(t, "0-3", f.runtime.get(f.sharedIDs[1]).String())
	f.prepare(f.newClaim)
}

func TestReconcileNoOpWithoutUpdater(t *testing.T) {
	f := newReconcileFixture(t, newFakeRuntime())
	defer f.stop()
	f.d.containerUpdater = nil

	f.unprepare(f.oldClaim)
	f.prepare(f.newClaim)
	require.Empty(t, f.runtime.allCalls())
}

func TestRequestReconcileCoalesces(t *testing.T) {
	d := &CPUDriver{reconcileTrigger: make(chan struct{}, 1)}
	for range 100 {
		d.requestReconcile()
	}
	require.Len(t, d.reconcileTrigger, 1)
}

func TestRequestReconcileIsInertWhenDisabled(t *testing.T) {
	d, _ := newMetricsTestDriverWithCPUInfos(t, mockCPUInfos_SingleSocket_4CPUS_HT)
	_, err := d.Synchronize(t.Context(), nil, nil)
	require.NoError(t, err)
	claim := individualMetricsClaim("claim", "cpudev0")
	results, err := d.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.NoError(t, results[claim.UID].Err)

	// Feature off: no trigger, no worker, Unprepare still has to succeed.
	unprepared, err := d.UnprepareResourceClaims(t.Context(), []kubeletplugin.NamespacedObject{{UID: claim.UID}})
	require.NoError(t, err)
	require.NoError(t, unprepared[claim.UID])
	_, found := d.allocationState.GetResourceClaimAllocation(claim.UID)
	require.False(t, found)
}

func TestAutoDetectStartsWorkerOnSafeRuntime(t *testing.T) {
	// Create driver with auto-detect config (nil).
	d, _ := newMetricsTestDriverWithCPUInfos(t, mockCPUInfos_SingleSocket_4CPUS_HT)

	// Create a fake runtime that implements containerUpdater.
	runtime := newFakeRuntime()

	// Simulate NRI Configure with a known-safe runtime.
	_, err := d.Configure(t.Context(), "", "containerd", "2.4.0")
	require.NoError(t, err)

	// Override lifecycleCtx with test context (Start() normally sets this).
	d.lifecycleCtx = t.Context()

	// Mark as auto-detect mode.
	d.configWasAuto = true

	// First Synchronize should detect and enable the feature, starting the worker.
	// Note: auto-detect will set containerUpdater = nriPlugin, but nriPlugin is nil in tests.
	_, err = d.Synchronize(t.Context(), nil, nil)
	require.NoError(t, err)

	// Verify reconcileTrigger was created and decision logged.
	require.NotNil(t, d.reconcileTrigger, "auto-detect should create reconcileTrigger")
	require.Contains(t, d.autoDetectDecision, "enabled", "decision should mention enabled")
	require.Contains(t, d.autoDetectDecision, "containerd 2.4.0", "decision should include detected version")

	// Set containerUpdater after Synchronize so the worker can call UpdateContainers.
	// This is a test-only workaround since we can't easily mock stub.Stub.
	d.containerUpdater = runtime

	// Verify the worker goroutine is responsive by triggering a reconcile.
	// The worker will find no shared containers to update, but it proves the goroutine is running.
	d.requestReconcile()

	// Wait briefly and verify we can send to the trigger again (worker drained it).
	time.Sleep(50 * time.Millisecond)
	select {
	case d.reconcileTrigger <- struct{}{}:
		// Successfully sent; worker consumed the first trigger
	case <-time.After(100 * time.Millisecond):
		t.Fatal("reconcileTrigger still full; worker may not be running")
	}
}

func TestAutoDetectDisablesOnUnsafeRuntime(t *testing.T) {
	d, _ := newMetricsTestDriverWithCPUInfos(t, mockCPUInfos_SingleSocket_4CPUS_HT)

	// Simulate NRI Configure with an older runtime.
	_, err := d.Configure(t.Context(), "", "containerd", "2.3.7")
	require.NoError(t, err)

	d.configWasAuto = true
	d.lifecycleCtx = t.Context()

	// First Synchronize should detect and disable the feature.
	_, err = d.Synchronize(t.Context(), nil, nil)
	require.NoError(t, err)

	require.Nil(t, d.reconcileTrigger, "auto-detect should NOT create reconcileTrigger on old runtime")
	require.Contains(t, d.autoDetectDecision, "disabled", "decision should mention disabled")
	require.Contains(t, d.autoDetectDecision, "predates", "reason should mention version too old")
}

func TestAutoDetectDisablesOnEmptyRuntimeName(t *testing.T) {
	d, _ := newMetricsTestDriverWithCPUInfos(t, mockCPUInfos_SingleSocket_4CPUS_HT)

	// Configure not called, or called with empty strings (old NRI stub).
	d.configWasAuto = true
	d.lifecycleCtx = t.Context()

	_, err := d.Synchronize(t.Context(), nil, nil)
	require.NoError(t, err)

	require.Nil(t, d.reconcileTrigger, "auto-detect should NOT enable without runtime info")
	require.Contains(t, d.autoDetectDecision, "not available", "decision should mention unavailable")
}
