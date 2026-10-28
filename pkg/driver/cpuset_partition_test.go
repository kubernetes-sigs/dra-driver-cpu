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
	"testing"

	"github.com/containerd/nri/pkg/api"
	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpupartition"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpupartition/cgrouptest"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
)

// These tests cover how the hooks drive the partition manager; the kernel
// side is tested in pkg/cpupartition.

func newPartitionTestDriver(t *testing.T, root string) (*CPUDriver, *prometheus.Registry) {
	t.Helper()
	cp, reg := newMetricsTestDriverWithCPUInfos(t, mockCPUInfos_SingleSocket_4CPUS_HT)
	var err error
	cp.partitions, err = cpupartition.New(cpupartition.Config{Mode: cpupartition.ModeRoot, CgroupRoot: root, Scope: "pods"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, cp.partitions.Close()) })
	return cp, reg
}

// preparePartitionClaim synchronizes an empty runtime and prepares claim
// "poc" for cpudev1, i.e. CPU 1.
func preparePartitionClaim(t *testing.T, cp *CPUDriver) *resourceapi.ResourceClaim {
	t.Helper()
	_, err := cp.Synchronize(t.Context(), nil, nil)
	require.NoError(t, err)
	claim := individualMetricsClaim("poc", "cpudev1")
	prepared, err := cp.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.NoError(t, prepared[claim.UID].Err)
	return claim
}

func partitionContainer(id string) (*api.PodSandbox, *api.Container) {
	pod := &api.PodSandbox{Id: "sandbox", Uid: "pod", Name: "pod"}
	return pod, &api.Container{Id: id, Name: "app", PodSandboxId: pod.Id, Env: []string{"DRA_CPUSET_poc=1"}, Linux: &api.LinuxContainer{CgroupsPath: "/pods/pod/" + id}}
}

func partitionsGauge(t *testing.T, reg *prometheus.Registry, state string) float64 {
	return metricValue(t, reg, "dra_cpu_cpuset_partitions", map[string]string{"state": state})
}

func TestPartitionHookLifecycle(t *testing.T) {
	root := t.TempDir()
	cp, reg := newPartitionTestDriver(t, root)
	claim := preparePartitionClaim(t, cp)
	pod, ctr := partitionContainer("first")
	cgrouptest.Create(t, root, "pods/pod/first", "1")
	_, _, err := cp.CreateContainer(t.Context(), pod, ctr)
	require.NoError(t, err)
	// Start uses the CPUs Create validated, not a changed environment.
	ctr.Env = []string{"DRA_CPUSET_forged=3"}
	require.NoError(t, cp.StartContainer(t.Context(), pod, ctr))
	require.Equal(t, "root", cgrouptest.Read(t, root, "pods/pod/first", "cpuset.cpus.partition"))
	require.Equal(t, "1", cgrouptest.Read(t, root, "pods", "cpuset.cpus.exclusive"))
	require.ErrorContains(t, cp.unprepareResourceClaim(t.Context(), testr.New(t), kubeletplugin.NamespacedObject{UID: claim.UID}), "cpuset partitions")

	ctr.State, ctr.Env = api.ContainerState_CONTAINER_RUNNING, []string{"DRA_CPUSET_poc=1"}
	_, err = cp.Synchronize(t.Context(), []*api.PodSandbox{pod}, []*api.Container{ctr})
	require.NoError(t, err)
	require.Equal(t, 1.0, partitionsGauge(t, reg, "valid"))
	require.Equal(t, "root", cgrouptest.Read(t, root, "pods/pod/first", "cpuset.cpus.partition"))

	// Stop releases the partition but keeps the claim for a restart.
	_, err = cp.StopContainer(t.Context(), pod, ctr)
	require.NoError(t, err)
	require.Equal(t, "member", cgrouptest.Read(t, root, "pods/pod/first", "cpuset.cpus.partition"))
	require.Empty(t, cgrouptest.Read(t, root, "pods", "cpuset.cpus.exclusive"))
	pod, ctr = partitionContainer("second")
	cgrouptest.Create(t, root, "pods/pod/second", "1")
	_, _, err = cp.CreateContainer(t.Context(), pod, ctr)
	require.NoError(t, err)
	require.NoError(t, cp.StartContainer(t.Context(), pod, ctr))
	require.NoError(t, cp.RemoveContainer(t.Context(), pod, ctr))

	released, err := cp.UnprepareResourceClaims(t.Context(), []kubeletplugin.NamespacedObject{{UID: claim.UID}})
	require.NoError(t, err)
	require.NoError(t, released[claim.UID])
	require.Empty(t, cgrouptest.Read(t, root, "pods/pod", "cpuset.cpus.exclusive"))
}

func TestPartitionStartFailsClosed(t *testing.T) {
	root := t.TempDir()
	cp, _ := newPartitionTestDriver(t, root)
	preparePartitionClaim(t, cp)
	pod, ctr := partitionContainer("ctr")
	// The runtime did not apply the cpuset CreateContainer asked for.
	cgrouptest.Create(t, root, "pods/pod/ctr", "0-3")
	_, _, err := cp.CreateContainer(t.Context(), pod, ctr)
	require.NoError(t, err)
	require.ErrorContains(t, cp.StartContainer(t.Context(), pod, ctr), "runtime cpuset")
	require.Empty(t, cgrouptest.Read(t, root, "pods", "cpuset.cpus.exclusive"))

	stale := &api.Container{Id: "unknown", Name: ctr.Name, PodSandboxId: pod.Id}
	require.ErrorContains(t, cp.StartContainer(t.Context(), pod, stale), "no CreateContainer state")
	shared := &api.Container{Id: "shared", Name: "sidecar", PodSandboxId: pod.Id}
	_, _, err = cp.CreateContainer(t.Context(), pod, shared)
	require.NoError(t, err)
	require.NoError(t, cp.StartContainer(t.Context(), pod, shared))
}

// A restarted driver has no leases. Synchronize adopts the live partition and
// removes a grant left by a container deleted while it was down.
func TestPartitionColdRestartAdoptsAndReclaims(t *testing.T) {
	root := t.TempDir()
	first, _ := newPartitionTestDriver(t, root)
	preparePartitionClaim(t, first)
	pod, ctr := partitionContainer("ctr")
	cgrouptest.Create(t, root, "pods/pod/ctr", "1")
	_, _, err := first.CreateContainer(t.Context(), pod, ctr)
	require.NoError(t, err)
	require.NoError(t, first.StartContainer(t.Context(), pod, ctr))
	cgrouptest.Create(t, root, "pods/gone", "")
	cgrouptest.Write(t, root, "pods/gone", "cpuset.cpus.exclusive", "3")
	cgrouptest.Write(t, root, "pods", "cpuset.cpus.exclusive", "1,3")

	restarted, reg := newPartitionTestDriver(t, root)
	restarted.cdiMgr = first.cdiMgr
	ctr.State = api.ContainerState_CONTAINER_RUNNING
	_, err = restarted.Synchronize(t.Context(), []*api.PodSandbox{pod}, []*api.Container{ctr})
	require.NoError(t, err)
	require.Equal(t, 1.0, partitionsGauge(t, reg, "valid"))
	require.Equal(t, "root", cgrouptest.Read(t, root, "pods/pod/ctr", "cpuset.cpus.partition"))
	require.Equal(t, "1", cgrouptest.Read(t, root, "pods", "cpuset.cpus.exclusive"))
	require.Empty(t, cgrouptest.Read(t, root, "pods/gone", "cpuset.cpus.exclusive"))
	require.Equal(t, []string{"ctr"}, restarted.partitions.Holding(first.cpuAllocationStore.GetPreparedCPUs()))
}
