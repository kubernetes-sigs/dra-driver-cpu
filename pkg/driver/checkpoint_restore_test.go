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
	"os"
	"path/filepath"
	"testing"

	"github.com/containerd/nri/pkg/api"
	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/cpuset"
)

// newRestartTestDriver creates a driver bound to checkpointDir, as a driver
// (re)start does. The boot ID is faked so the test also runs off Linux.
func newRestartTestDriver(t *testing.T, checkpointDir string) (*CPUDriver, error) {
	t.Helper()
	infos := mockCPUInfos_SingleSocket_4CPUS_HT
	return New(testr.New(t), Providers{
		CPUInfo: &cpuinfo.MockCPUInfoProvider{CPUInfos: infos},
		SysFS:   testSysFS(infos),
	}, &Config{
		DriverName:    testDriverName,
		NodeName:      testNodeName,
		CheckpointDir: checkpointDir,
	})
}

func setTestBootID(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOST_ROOT", root)
	bootIDPath := filepath.Join(root, "proc", "sys", "kernel", "random", "boot_id")
	require.NoError(t, os.MkdirAll(filepath.Dir(bootIDPath), 0o755))
	require.NoError(t, os.WriteFile(bootIDPath, []byte("f8425f61-6408-4e07-8dfe-717b60b682b6\n"), 0o600))
}

func TestFirstSynchronizeAfterRestartUsesCheckpoint(t *testing.T) {
	setTestBootID(t)
	dir := filepath.Join(t.TempDir(), "checkpoints")
	logger := testr.New(t)

	before, err := newRestartTestDriver(t, dir)
	require.NoError(t, err)
	require.False(t, before.checkpointRestored, "no checkpoint exists on the first start")
	// "a" is bound to a running container, "pending" is prepared but its container is not running.
	require.NoError(t, before.allocationState.Reserve(logger, before.checkpointMgr, store.ClaimIdentifier{UID: "a"}, fullClaimRequest(cpuset.New(0)), false))
	_, err = before.allocationState.Bind(logger, before.checkpointMgr, "pod", "app", map[store.ClaimRequest]cpuset.CPUSet{{ClaimUID: "a"}: cpuset.New(0)})
	require.NoError(t, err)
	require.NoError(t, before.allocationState.Reserve(logger, before.checkpointMgr, store.ClaimIdentifier{UID: "pending"}, fullClaimRequest(cpuset.New(2)), false))

	after, err := newRestartTestDriver(t, dir)
	require.NoError(t, err)
	require.True(t, after.checkpointRestored)
	require.False(t, after.hasSynchronized)
	require.Equal(t, "0,2", after.allocationState.GetPreparedCPUs().String())

	// "b" has CDI and env but was unprepared before the restart: the runtime
	// must not resurrect it on the first Synchronize.
	cdi := newMockCdiMgrWithAllocations(map[types.UID]cpuset.CPUSet{"a": cpuset.New(0), "b": cpuset.New(1)})
	after.cdiMgr = cdi
	pod := &api.PodSandbox{Id: "sandbox", Uid: "pod"}
	app := &api.Container{Id: "app", Name: "app", PodSandboxId: pod.Id, Env: []string{"DRA_CPUSET_a=0"}}
	stale := &api.Container{Id: "stale", Name: "stale", PodSandboxId: pod.Id, Env: []string{"DRA_CPUSET_b=1"}}

	updates, err := after.Synchronize(t.Context(), []*api.PodSandbox{pod}, []*api.Container{app, stale})
	require.NoError(t, err)
	require.True(t, after.hasSynchronized)
	require.Equal(t, "0,2", after.allocationState.GetPreparedCPUs().String())
	_, prepared := after.allocationState.GetResourceClaimAllocation("b")
	require.False(t, prepared)
	require.True(t, after.allocationState.IsOwner("pod", "app", store.ClaimRequest{ClaimUID: "a"}))
	_, prepared = after.allocationState.GetResourceClaimAllocation("pending")
	require.True(t, prepared, "a prepared claim without a running container must stay reserved")

	masks := map[string]string{}
	for _, u := range updates {
		masks[u.GetContainerId()] = u.GetLinux().GetResources().GetCpu().GetCpus()
	}
	require.Equal(t, map[string]string{"app": "0", "stale": "1,3"}, masks)
}

func TestFirstSynchronizeWithoutCheckpointRebuildsFromRuntime(t *testing.T) {
	setTestBootID(t)
	dir := filepath.Join(t.TempDir(), "checkpoints")

	d, err := newRestartTestDriver(t, dir)
	require.NoError(t, err)
	require.False(t, d.checkpointRestored)
	require.True(t, d.allocationState.GetPreparedCPUs().IsEmpty())

	// No checkpoint (first start, upgrade): the runtime env and CDI are the only record.
	d.cdiMgr = newMockCdiMgrWithAllocations(map[types.UID]cpuset.CPUSet{"a": cpuset.New(0)})
	pod := &api.PodSandbox{Id: "sandbox", Uid: "pod"}
	app := &api.Container{Id: "app", Name: "app", PodSandboxId: pod.Id, Env: []string{"DRA_CPUSET_a=0"}}
	_, err = d.Synchronize(t.Context(), []*api.PodSandbox{pod}, []*api.Container{app})
	require.NoError(t, err)
	require.Equal(t, "0", d.allocationState.GetPreparedCPUs().String())
	require.True(t, d.allocationState.IsOwner("pod", "app", store.ClaimRequest{ClaimUID: "a"}))

	// Synchronize wrote the rebuilt state, so the next start restores it.
	next, err := newRestartTestDriver(t, dir)
	require.NoError(t, err)
	require.True(t, next.checkpointRestored)
	require.Equal(t, "0", next.allocationState.GetPreparedCPUs().String())
	require.True(t, next.allocationState.IsOwner("pod", "app", store.ClaimRequest{ClaimUID: "a"}))
}

// The runtime data (env and CDI) agree with each other but not with the
// checkpoint. This should not happen; if it does, the checkpoint wins and the
// container is skipped instead of failing recovery for the whole node.
func TestFirstSynchronizeAfterRestartSkipsRuntimeDisagreeingWithCheckpoint(t *testing.T) {
	testCases := []struct {
		name      string
		container *api.Container
		cdiCPUs   cpuset.CPUSet
	}{
		{
			name:      "different CPUs",
			container: &api.Container{Id: "app", Name: "app", PodSandboxId: "sandbox", Env: []string{"DRA_CPUSET_a=1"}},
			cdiCPUs:   cpuset.New(1),
		},
		{
			name:      "different owner",
			container: &api.Container{Id: "other", Name: "other", PodSandboxId: "sandbox", Env: []string{"DRA_CPUSET_a=0"}},
			cdiCPUs:   cpuset.New(0),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			setTestBootID(t)
			dir := filepath.Join(t.TempDir(), "checkpoints")
			logger := testr.New(t)
			before, err := newRestartTestDriver(t, dir)
			require.NoError(t, err)
			require.NoError(t, before.allocationState.Reserve(logger, before.checkpointMgr, store.ClaimIdentifier{UID: "a"}, fullClaimRequest(cpuset.New(0)), false))
			_, err = before.allocationState.Bind(logger, before.checkpointMgr, "pod", "app", map[store.ClaimRequest]cpuset.CPUSet{{ClaimUID: "a"}: cpuset.New(0)})
			require.NoError(t, err)

			after, err := newRestartTestDriver(t, dir)
			require.NoError(t, err)
			require.True(t, after.checkpointRestored)
			after.cdiMgr = newMockCdiMgrWithAllocations(map[types.UID]cpuset.CPUSet{"a": tc.cdiCPUs})
			pod := &api.PodSandbox{Id: "sandbox", Uid: "pod"}
			sidecar := &api.Container{Id: "sidecar", Name: "sidecar", PodSandboxId: pod.Id}

			_, err = after.Synchronize(t.Context(), []*api.PodSandbox{pod}, []*api.Container{tc.container, sidecar})
			require.NoError(t, err)
			require.True(t, after.hasSynchronized)
			cpus, found := after.allocationState.GetResourceClaimAllocation("a")
			require.True(t, found)
			require.Equal(t, "0", cpus.String())
			require.True(t, after.allocationState.IsOwner("pod", "app", store.ClaimRequest{ClaimUID: "a"}))
			for _, name := range []string{tc.container.Name, sidecar.Name} {
				state := after.podState.GetContainerState("pod", name)
				require.NotNil(t, state, "container %q was not recovered", name)
				require.False(t, state.HasExclusiveCPUAllocation(), "container %q must not be exclusive", name)
			}
		})
	}
}

func TestNewFailsOnCorruptCheckpoint(t *testing.T) {
	setTestBootID(t)
	dir := filepath.Join(t.TempDir(), "checkpoints")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "allocation_state"), []byte{0xff}, 0o600))

	_, err := newRestartTestDriver(t, dir)
	require.ErrorContains(t, err, "failed to load allocation state checkpoint")
}

func TestFirstSynchronizeAfterRestartMultiRequestClaim(t *testing.T) {
	t.Run("recovers bound and sibling requests while skipping conflicts", func(t *testing.T) {
		setTestBootID(t)
		dir := filepath.Join(t.TempDir(), "checkpoints")
		logger := testr.New(t)

		before, err := newRestartTestDriver(t, dir)
		require.NoError(t, err)
		require.NoError(t, before.allocationState.Reserve(logger, before.checkpointMgr, store.ClaimIdentifier{UID: "a"}, map[string]cpuset.CPUSet{
			"req-1": cpuset.New(0),
			"req-2": cpuset.New(1),
		}, false))
		_, err = before.allocationState.Bind(logger, before.checkpointMgr, "pod", "ctr-1", map[store.ClaimRequest]cpuset.CPUSet{
			{ClaimUID: "a", Request: "req-1"}: cpuset.New(0),
		})
		require.NoError(t, err)

		after, err := newRestartTestDriver(t, dir)
		require.NoError(t, err)
		require.True(t, after.checkpointRestored)
		after.cdiMgr = newMockCdiMgrWithRequestAllocations(map[types.UID]map[string]cpuset.CPUSet{
			"a": {"req-1": cpuset.New(0), "req-2": cpuset.New(1)},
		})

		pod := &api.PodSandbox{Id: "sandbox", Uid: "pod"}
		otherPod := &api.PodSandbox{Id: "other-sandbox", Uid: "other-pod"}
		ctr1 := &api.Container{Id: "ctr-1", Name: "ctr-1", PodSandboxId: pod.Id, Env: []string{"DRA_CPUSET_a_req-1=0"}}
		ctr2 := &api.Container{Id: "ctr-2", Name: "ctr-2", PodSandboxId: pod.Id, Env: []string{"DRA_CPUSET_a_req-2=1"}}
		dup := &api.Container{Id: "dup", Name: "dup", PodSandboxId: pod.Id, Env: []string{"DRA_CPUSET_a_req-1=0"}}
		crossPod := &api.Container{Id: "cross", Name: "cross", PodSandboxId: otherPod.Id, Env: []string{"DRA_CPUSET_a_req-2=1"}}

		updates, err := after.Synchronize(t.Context(), []*api.PodSandbox{pod, otherPod}, []*api.Container{ctr1, ctr2, dup, crossPod})
		require.NoError(t, err)
		require.True(t, after.hasSynchronized)
		require.True(t, after.allocationState.IsOwner("pod", "ctr-1", store.ClaimRequest{ClaimUID: "a", Request: "req-1"}))
		require.True(t, after.allocationState.IsOwner("pod", "ctr-2", store.ClaimRequest{ClaimUID: "a", Request: "req-2"}))
		require.False(t, after.allocationState.IsOwner("pod", "dup", store.ClaimRequest{ClaimUID: "a", Request: "req-1"}))
		require.False(t, after.allocationState.IsOwner("other-pod", "cross", store.ClaimRequest{ClaimUID: "a", Request: "req-2"}))

		masks := map[string]string{}
		for _, u := range updates {
			masks[u.GetContainerId()] = u.GetLinux().GetResources().GetCpu().GetCpus()
		}
		require.Equal(t, map[string]string{
			"ctr-1": "0",
			"ctr-2": "1",
			"dup":   "2-3",
			"cross": "2-3",
		}, masks)
	})

	t.Run("skips runtime whose per-request split disagrees with checkpoint", func(t *testing.T) {
		setTestBootID(t)
		dir := filepath.Join(t.TempDir(), "checkpoints")
		logger := testr.New(t)

		before, err := newRestartTestDriver(t, dir)
		require.NoError(t, err)
		require.NoError(t, before.allocationState.Reserve(logger, before.checkpointMgr, store.ClaimIdentifier{UID: "a"}, map[string]cpuset.CPUSet{
			"req-1": cpuset.New(0),
			"req-2": cpuset.New(1),
		}, false))
		_, err = before.allocationState.Bind(logger, before.checkpointMgr, "pod", "ctr-1", map[store.ClaimRequest]cpuset.CPUSet{
			{ClaimUID: "a", Request: "req-1"}: cpuset.New(0),
		})
		require.NoError(t, err)

		after, err := newRestartTestDriver(t, dir)
		require.NoError(t, err)
		require.True(t, after.checkpointRestored)
		// Same claim union ("0-1"), but per-request CPUs are swapped relative to the checkpoint.
		after.cdiMgr = newMockCdiMgrWithRequestAllocations(map[types.UID]map[string]cpuset.CPUSet{
			"a": {"req-1": cpuset.New(1), "req-2": cpuset.New(0)},
		})

		pod := &api.PodSandbox{Id: "sandbox", Uid: "pod"}
		ctr1 := &api.Container{Id: "ctr-1", Name: "ctr-1", PodSandboxId: pod.Id, Env: []string{"DRA_CPUSET_a_req-1=1"}}
		sidecar := &api.Container{Id: "sidecar", Name: "sidecar", PodSandboxId: pod.Id}

		_, err = after.Synchronize(t.Context(), []*api.PodSandbox{pod}, []*api.Container{ctr1, sidecar})
		require.NoError(t, err)
		gotRequests, found := after.allocationState.GetResourceClaimRequests("a")
		require.True(t, found)
		require.Equal(t, map[string]cpuset.CPUSet{"req-1": cpuset.New(0), "req-2": cpuset.New(1)}, gotRequests)
		require.True(t, after.allocationState.IsOwner("pod", "ctr-1", store.ClaimRequest{ClaimUID: "a", Request: "req-1"}))
		for _, name := range []string{ctr1.Name, sidecar.Name} {
			state := after.podState.GetContainerState("pod", name)
			require.NotNil(t, state, "container %q was not recovered", name)
			require.False(t, state.HasExclusiveCPUAllocation(), "container %q must not be exclusive", name)
		}
	})
}
