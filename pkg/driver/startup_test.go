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
	"fmt"
	"testing"
	"time"

	"github.com/containerd/nri/pkg/api"
	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/utils/cpuset"
)

// Construct a fresh process with an empty allocation store, but CDI and runtime
// inventory from an existing exclusive container. Delay the Synchronize call
// itself, not a callback already holding stateMu: this is the startup window.
// The initial sync wait is short so specs that expect a rejection stay fast.
func startupWindowFixture(t *testing.T) (*CPUDriver, *api.PodSandbox, []*api.Container, *resourceapi.ResourceClaim) {
	t.Helper()
	infos := mockCPUInfos_SingleSocket_4CPUS_HT
	driver, err := New(testr.New(t), Providers{CPUInfo: &cpuinfo.MockCPUInfoProvider{CPUInfos: infos}, SysFS: testSysFS(infos)}, &Config{
		DriverName: testDriverName, NodeName: testNodeName,
		CPUDeviceMode: device.CPU_DEVICE_MODE_GROUPED, CPUDeviceGroupBy: device.GROUP_BY_NUMA_NODE,
	})
	require.NoError(t, err)
	driver.initialSyncWait = 10 * time.Millisecond
	held := cpuset.New(0, 2) // Deterministic first choice of this allocator/topology.
	driver.cdiMgr = newMockCdiMgrWithAllocations(map[types.UID]cpuset.CPUSet{"existing": held})
	pod := &api.PodSandbox{Id: "existing-pod", Uid: "existing-pod-uid"}
	containers := []*api.Container{{Id: "existing-container", Name: "workload", PodSandboxId: pod.Id, State: api.ContainerState_CONTAINER_RUNNING, Env: []string{fmt.Sprintf("%s_existing=%s", cdiEnvVarPrefix, held.String())}}}
	claim := testClaim("new-claim", testDriverName, testNodeName, map[string]int64{"cpudevnuma000": 2})
	return driver, pod, containers, claim
}

func TestStartupWindowPrepareMustWaitForRecovery(t *testing.T) {
	driver, pod, containers, claim := startupWindowFixture(t)
	before := len(driver.cdiMgr.(*mockCdiMgr).devices)
	results, err := driver.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.ErrorContains(t, results[claim.UID].Err, "initial NRI synchronization", "Prepare must reject a request before the first successful Synchronize")
	_, ok := driver.allocationState.GetResourceClaimAllocation(claim.UID)
	require.False(t, ok)
	require.Len(t, driver.cdiMgr.(*mockCdiMgr).devices, before, "rejection must not write CDI")
	_, err = driver.Synchronize(t.Context(), []*api.PodSandbox{pod}, containers)
	require.NoError(t, err)
	require.True(t, driver.hasSynchronized)
	results, err = driver.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.NoError(t, results[claim.UID].Err)
	allocated, ok := driver.allocationState.GetResourceClaimAllocation(claim.UID)
	require.True(t, ok)
	require.True(t, allocated.Intersection(cpuset.New(0, 2)).IsEmpty())
	require.Equal(t, 2, allocated.Size())
	t.Logf("after recovery: existing=0,2, new=%s", allocated.String())
}

// A Prepare that arrives just before the first Synchronize waits for it and then
// succeeds on the first attempt, instead of failing and relying on kubelet retry.
func TestStartupWindowPrepareWaitsForInitialSynchronize(t *testing.T) {
	driver, pod, containers, claim := startupWindowFixture(t)
	driver.initialSyncWait = time.Minute // Only the Synchronize below may release Prepare.
	done := make(chan prepareOutcome, 1)
	go func() {
		results, err := driver.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
		done <- prepareOutcome{results: results, err: err}
	}()
	require.Never(t, func() bool { return len(done) > 0 }, 100*time.Millisecond, 10*time.Millisecond, "Prepare must wait for the first Synchronize")

	_, err := driver.Synchronize(t.Context(), []*api.PodSandbox{pod}, containers)
	require.NoError(t, err)
	var outcome prepareOutcome
	require.Eventually(t, func() bool {
		select {
		case outcome = <-done:
			return true
		default:
			return false
		}
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, outcome.err)
	require.NoError(t, outcome.results[claim.UID].Err)
	allocated, ok := driver.allocationState.GetResourceClaimAllocation(claim.UID)
	require.True(t, ok)
	require.True(t, allocated.Intersection(cpuset.New(0, 2)).IsEmpty())
}

type prepareOutcome struct {
	results map[types.UID]kubeletplugin.PrepareResult
	err     error
}

func TestStartupWindowFailedSynchronizeKeepsGateClosed(t *testing.T) {
	driver, pod, containers, claim := startupWindowFixture(t)
	driver.cdiMgr.(*mockCdiMgr).devices[getCDIDeviceName("conflict")] = fmt.Sprintf("%s_conflict=0,2", cdiEnvVarPrefix)
	bad := append([]*api.Container{}, containers...)
	bad = append(bad, &api.Container{Id: "conflict-container", Name: "other", PodSandboxId: pod.Id, State: api.ContainerState_CONTAINER_RUNNING, Env: []string{fmt.Sprintf("%s_conflict=0,2", cdiEnvVarPrefix)}})
	_, err := driver.Synchronize(t.Context(), []*api.PodSandbox{pod}, bad)
	require.Error(t, err)
	require.False(t, driver.hasSynchronized)
	results, err := driver.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.Error(t, results[claim.UID].Err)
	_, ok := driver.allocationState.GetResourceClaimAllocation(claim.UID)
	require.False(t, ok)
	_, err = driver.Synchronize(t.Context(), []*api.PodSandbox{pod}, containers)
	require.NoError(t, err)
	results, err = driver.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.NoError(t, results[claim.UID].Err)
}
