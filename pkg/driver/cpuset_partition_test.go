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
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/containerd/nri/pkg/api"
	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	cpumetrics "github.com/kubernetes-sigs/dra-driver-cpu/pkg/metrics"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/utils/cpuset"
)

func TestPartitionPath(t *testing.T) {
	for raw, want := range map[string]string{
		"/kubepods/pod/ctr": "kubepods/pod/ctr",
		"kubepods-burstable-pod123.slice:cri-containerd:abc": "kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod123.slice/cri-containerd-abc.scope",
		"kubepods-pod123.slice:crio:abc":                     "kubepods.slice/kubepods-pod123.slice/crio-abc.scope",
	} {
		got, err := partitionPath(raw)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	for _, raw := range []string{"", "/", "../escape", "/a/../b", "/a/./b", "/a//b", "bad:prefix:id", "a.slice:../x:id", "a.slice:x:../id"} {
		_, err := partitionPath(raw)
		require.Error(t, err, raw)
	}
}

func newPartitionDriver(t *testing.T, root, mode string, all cpuset.CPUSet) *CPUDriver {
	t.Helper()
	var infos []cpuinfo.CPUInfo
	for _, id := range all.List() {
		infos = append(infos, cpuinfo.CPUInfo{CpuID: id, CoreID: id, SocketID: 0, NUMANodeID: 0})
	}
	topo, err := (&cpuinfo.MockCPUInfoProvider{CPUInfos: infos}).GetCPUTopology(testr.New(t))
	require.NoError(t, err)
	partitions, err := newCPUSetPartitions(root, mode)
	require.NoError(t, err)
	cp := &CPUDriver{partitions: partitions, podConfigStore: store.NewPodConfig(), cpuAllocationStore: store.NewCPUAllocation(topo, cpuset.New()), claimTracker: store.NewClaimTracker(), metrics: cpumetrics.Noop()}
	t.Cleanup(func() {
		for id := range partitions.leases {
			if err := partitions.release(id); err != nil {
				t.Logf("partition cleanup: %v", err)
			}
		}
		for _, node := range partitions.nodes {
			_ = node.dir.Close()
		}
	})
	return cp
}

func partitionContainer(t *testing.T, cp *CPUDriver, id, path string, cpus cpuset.CPUSet) (*api.PodSandbox, *api.Container) {
	t.Helper()
	uid := types.UID("claim-" + id)
	require.NoError(t, cp.cpuAllocationStore.ReserveResourceClaimAllocation(testr.New(t), uid, cpus, false))
	pod := &api.PodSandbox{Id: "sandbox-" + id, Uid: "pod-" + id, Name: "pod-" + id}
	ctr := &api.Container{Id: id, Name: "app", PodSandboxId: pod.Id, Env: []string{fmt.Sprintf("%s_%s=%s", cdiEnvVarPrefix, uid, cpus.String())}, Linux: &api.LinuxContainer{CgroupsPath: path}}
	adj, _, err := cp.CreateContainer(context.Background(), pod, ctr)
	require.NoError(t, err)
	require.Equal(t, cpus.String(), adj.Linux.Resources.Cpu.Cpus)
	return pod, ctr
}

// Regular files exercise bookkeeping and error paths, not kernel semantics.
func fakePartitionTree(t *testing.T, root, path string, cpus cpuset.CPUSet) {
	t.Helper()
	parts := strings.Split(path, "/")
	for i := range parts {
		dir := filepath.Join(append([]string{root}, parts[:i+1]...)...)
		require.NoError(t, os.MkdirAll(dir, 0750))
		for name, value := range map[string]string{"cpuset.cpus.exclusive": "", "cpuset.cpus.partition": "member", "cpuset.cpus": "0-7", "cpuset.cpus.exclusive.effective": "0-7", "cpuset.cpus.effective": "0-7"} {
			if i == len(parts)-1 && name != "cpuset.cpus.partition" && name != "cpuset.cpus.exclusive" {
				value = cpus.String()
			}
			file := filepath.Join(dir, name)
			if _, err := os.Stat(file); os.IsNotExist(err) {
				require.NoError(t, os.WriteFile(file, []byte(value+"\n"), 0600))
			}
		}
	}
}

func readPartitionFile(t *testing.T, root, path, name string) string {
	t.Helper()
	dir, err := os.OpenRoot(root)
	require.NoError(t, err)
	defer dir.Close()
	b, err := dir.ReadFile(filepath.Join(path, name))
	require.NoError(t, err)
	return strings.TrimSpace(string(b))
}

func TestPartitionLifecycle(t *testing.T) {
	root := t.TempDir()
	cp := newPartitionDriver(t, root, "root", cpuset.New(0, 1, 2, 3))
	fakePartitionTree(t, root, "pool/pod/one", cpuset.New(1))
	fakePartitionTree(t, root, "pool/pod/two", cpuset.New(2))
	pod, ctr := partitionContainer(t, cp, "one", "pool/pod/one", cpuset.New(1))
	pod2, ctr2 := partitionContainer(t, cp, "two", "pool/pod/two", cpuset.New(2))
	// Start derives CPUs from validated Create state, not a later env rewrite.
	ctr.Env = []string{cdiEnvVarPrefix + "_forged=3"}
	require.NoError(t, cp.StartContainer(context.Background(), pod, ctr))
	require.NoError(t, cp.StartContainer(context.Background(), pod, ctr))
	require.NoError(t, cp.StartContainer(context.Background(), pod2, ctr2))
	require.Equal(t, "1-2", readPartitionFile(t, root, "pool/pod", "cpuset.cpus.exclusive"))
	require.ErrorContains(t, cp.unprepareResourceClaim(context.Background(), testr.New(t), kubeletplugin.NamespacedObject{UID: "claim-one"}), "partition state")
	ctr.State = api.ContainerState_CONTAINER_RUNNING
	ctr2.State = api.ContainerState_CONTAINER_RUNNING
	require.NoError(t, cp.synchronizePartitions([]*api.PodSandbox{pod, pod2}, []*api.Container{ctr, ctr2}, cp.podConfigStore))
	_, err := cp.StopContainer(context.Background(), pod, ctr)
	require.NoError(t, err)
	require.Equal(t, "2", readPartitionFile(t, root, "pool/pod", "cpuset.cpus.exclusive"))
	require.Equal(t, "member", readPartitionFile(t, root, "pool/pod/one", "cpuset.cpus.partition"))
	_, ok := cp.cpuAllocationStore.GetResourceClaimAllocation("claim-one")
	require.True(t, ok)
	// A fresh runtime ID must establish partition state again.
	ctr.Id = "replacement"
	ctr.Env = []string{cdiEnvVarPrefix + "_claim-one=1"}
	_, _, err = cp.CreateContainer(context.Background(), pod, ctr)
	require.NoError(t, err)
	require.NoError(t, cp.StartContainer(context.Background(), pod, ctr))
	stale := api.Container{Id: "one", Name: ctr.Name, PodSandboxId: ctr.PodSandboxId}
	require.NoError(t, cp.RemoveContainer(context.Background(), pod, &stale))
	require.Equal(t, "root", readPartitionFile(t, root, "pool/pod/one", "cpuset.cpus.partition"))
	require.NoError(t, cp.RemoveContainer(context.Background(), pod, ctr))
	require.NoError(t, cp.RemoveContainer(context.Background(), pod2, ctr2))
	require.Empty(t, cp.partitions.leases)
	require.Empty(t, cp.partitions.nodes)
	require.Empty(t, readPartitionFile(t, root, "pool", "cpuset.cpus.exclusive"))
}

func TestPartitionRollbackAndConflicts(t *testing.T) {
	for _, failure := range []string{"missing-grant", "runtime-mask", "foreign-mask", "foreign-partition", "symlink"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			cp := newPartitionDriver(t, root, "isolated", cpuset.New(0, 1, 2, 3))
			path := "pool/pod/ctr"
			fakePartitionTree(t, root, path, cpuset.New(1))
			switch failure {
			case "missing-grant":
				require.NoError(t, os.WriteFile(filepath.Join(root, "pool/pod/cpuset.cpus.exclusive.effective"), []byte("\n"), 0600))
			case "runtime-mask":
				require.NoError(t, os.WriteFile(filepath.Join(root, path, "cpuset.cpus"), []byte("2\n"), 0600))
			case "foreign-mask":
				require.NoError(t, os.WriteFile(filepath.Join(root, path, "cpuset.cpus.exclusive"), []byte("2\n"), 0600))
			case "foreign-partition":
				require.NoError(t, os.WriteFile(filepath.Join(root, path, "cpuset.cpus.partition"), []byte("root\n"), 0600))
			case "symlink":
				require.NoError(t, os.Rename(filepath.Join(root, "pool"), filepath.Join(t.TempDir(), "foreign")))
				require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(root, "pool")))
			}
			pod, ctr := partitionContainer(t, cp, "one", path, cpuset.New(1))
			require.Error(t, cp.StartContainer(context.Background(), pod, ctr))
			require.Empty(t, cp.partitions.leases)
			require.Empty(t, cp.partitions.nodes)
			if failure != "symlink" {
				require.Empty(t, readPartitionFile(t, root, "pool", "cpuset.cpus.exclusive"))
			}
		})
	}
}

// Common single-container setup keeps failure tests focused on the transition
// they exercise. It intentionally does not activate the partition.
func partitionFixture(t *testing.T) (string, *CPUDriver, *api.PodSandbox, *api.Container) {
	t.Helper()
	root := t.TempDir()
	cp := newPartitionDriver(t, root, "root", cpuset.New(0, 1, 2, 3))
	path := "pool/pod/ctr"
	fakePartitionTree(t, root, path, cpuset.New(1))
	pod, ctr := partitionContainer(t, cp, "one", path, cpuset.New(1))
	return root, cp, pod, ctr
}

func TestPartitionRejectsInvalidReadbackAndReplacement(t *testing.T) {
	root, cp, pod, ctr := partitionFixture(t)
	path := ctr.Linux.CgroupsPath
	require.NoError(t, cp.StartContainer(context.Background(), pod, ctr))
	require.NoError(t, os.WriteFile(filepath.Join(root, path, "cpuset.cpus.partition"), []byte("root invalid (test)\n"), 0600))
	require.Error(t, cp.StartContainer(context.Background(), pod, ctr))
	require.NoError(t, cp.partitions.release(ctr.Id))
	require.NoError(t, cp.StartContainer(context.Background(), pod, ctr))
	old := filepath.Join(root, "old")
	require.NoError(t, os.Rename(filepath.Join(root, path), old))
	fakePartitionTree(t, root, path, cpuset.New(1))
	require.ErrorContains(t, cp.partitions.release(ctr.Id), "replaced")
	require.Equal(t, "member", readPartitionFile(t, root, path, "cpuset.cpus.partition"))
	require.Equal(t, "1", readPartitionFile(t, root, "pool", "cpuset.cpus.exclusive"))
	require.NoError(t, os.RemoveAll(filepath.Join(root, path)))
	require.NoError(t, os.Rename(old, filepath.Join(root, path)))
	require.NoError(t, cp.partitions.release(ctr.Id))
}

func TestPartitionSynchronizationAndAdmission(t *testing.T) {
	_, cp, pod, ctr := partitionFixture(t)
	ctr.State = api.ContainerState_CONTAINER_RUNNING
	require.ErrorContains(t, cp.synchronizePartitions([]*api.PodSandbox{pod}, []*api.Container{ctr}, cp.podConfigStore), "cold adoption")
	require.NoError(t, cp.StartContainer(context.Background(), pod, ctr))
	require.ErrorContains(t, cp.synchronizePartitions(nil, []*api.Container{ctr}, cp.podConfigStore), "no pod")
	require.NotNil(t, cp.partitions.leases[ctr.Id])
	require.ErrorContains(t, cp.synchronizePartitions([]*api.PodSandbox{pod}, []*api.Container{ctr}, store.NewPodConfig()), "lost trusted")
	require.NoError(t, cp.synchronizePartitions(nil, nil, store.NewPodConfig()))
	require.Empty(t, cp.partitions.leases)
	stale := api.Container{Id: "unknown", Name: ctr.Name, PodSandboxId: ctr.PodSandboxId}
	require.Error(t, cp.StartContainer(context.Background(), pod, &stale))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, cp.StartContainer(ctx, pod, ctr), context.Canceled)
	cp.partitions = nil
	require.NoError(t, cp.StartContainer(context.Background(), pod, &stale))
}

// Opt-in real-kernel test. Run the driver test binary as root on a disposable
// Linux host: DRACPU_PARTITION_TEST_ROOT=/sys/fs/cgroup ./driver.test
// -test.run TestPartitionKernelLifecycle -test.v
// It creates only its own subtree, never enables controllers above that subtree,
// and calls the same Create/Start/Stop/Remove hooks as NRI. This is not a CRI E2E.
func TestPartitionKernelLifecycle(t *testing.T) {
	root := os.Getenv("DRACPU_PARTITION_TEST_ROOT")
	if root == "" {
		t.Skip("set DRACPU_PARTITION_TEST_ROOT on a disposable cgroup-v2 host")
	}
	require.Equal(t, "linux", runtime.GOOS)
	require.Equal(t, 0, os.Geteuid())
	all, err := cpuset.Parse(readPartitionFile(t, root, "", "cpuset.cpus.effective"))
	require.NoError(t, err)
	require.GreaterOrEqual(t, all.Size(), 3)
	cpus := cpuset.New(all.List()[all.Size()-1])
	if selected := os.Getenv("DRACPU_PARTITION_TEST_CPUS"); selected != "" {
		cpus, err = cpuset.Parse(selected)
		require.NoError(t, err)
		require.False(t, cpus.IsEmpty())
		require.True(t, cpus.IsSubsetOf(all))
		require.Less(t, cpus.Size(), all.Size())
	}
	otherCPUs := all.Difference(cpus)
	for _, mode := range []string{"root", "isolated"} {
		for _, qos := range []string{"guaranteed", "burstable", "besteffort"} {
			t.Run(mode+"/"+qos, func(t *testing.T) {
				base, err := os.MkdirTemp(root, "dra330-go-")
				require.NoError(t, err)
				var dirs []string
				dirs = append(dirs, base)
				t.Cleanup(func() {
					for i := len(dirs) - 1; i >= 0; i-- {
						if err := os.Remove(dirs[i]); err != nil { //nolint:gosec // Only paths created by this opt-in disposable-host test.
							t.Errorf("cleanup %s: %v", dirs[i], err)
						}
					}
				})
				makeGroup := func(parent, name string, children bool) string {
					path := filepath.Join(parent, name)
					require.NoError(t, os.Mkdir(path, 0750)) //nolint:gosec // Child of the uniquely created test cgroup.
					dirs = append(dirs, path)
					if children {
						writePartitionFile(t, path, "cgroup.subtree_control", "+cpuset")
					}
					return path
				}
				writePartitionFile(t, base, "cgroup.subtree_control", "+cpuset")
				parent := base
				if qos != "guaranteed" {
					parent = makeGroup(base, qos, true)
				}
				podGroup := makeGroup(parent, "pod", true)
				leaf := makeGroup(podGroup, "container", false)
				shared := makeGroup(base, "shared", false)
				// An ordinary sibling with a stale explicit mask must lose exclusive CPUs.
				writePartitionFile(t, shared, "cpuset.cpus", all.String())
				sleeper := exec.Command("sleep", "300")
				require.NoError(t, sleeper.Start())
				t.Cleanup(func() { _ = sleeper.Process.Kill(); _ = sleeper.Wait() })
				writePartitionFile(t, shared, "cgroup.procs", fmt.Sprint(sleeper.Process.Pid))
				cp := newPartitionDriver(t, root, mode, all)
				relative, err := filepath.Rel(root, leaf)
				require.NoError(t, err)
				pod, ctr := partitionContainer(t, cp, "kernel-"+qos, relative, cpus)
				workload := startPartitionTask(t, cp, pod, ctr, leaf, cpus)
				require.Equal(t, mode, readPartitionFile(t, root, relative, "cpuset.cpus.partition"))
				require.Equal(t, otherCPUs.String(), readPartitionFile(t, shared, "", "cpuset.cpus.effective"))
				ctr.State = api.ContainerState_CONTAINER_RUNNING
				require.NoError(t, cp.synchronizePartitions([]*api.PodSandbox{pod}, []*api.Container{ctr}, cp.podConfigStore))
				require.NoError(t, workload.Process.Kill())
				_ = workload.Wait()
				_, err = cp.StopContainer(context.Background(), pod, ctr)
				require.NoError(t, err)
				require.Equal(t, all.String(), readPartitionFile(t, shared, "", "cpuset.cpus.effective"))
				require.NoError(t, os.Remove(leaf))
				require.NoError(t, os.Mkdir(leaf, 0750))
				ctr.Id += "-replacement"
				_, _, err = cp.CreateContainer(context.Background(), pod, ctr)
				require.NoError(t, err)
				workload = startPartitionTask(t, cp, pod, ctr, leaf, cpus)
				require.NoError(t, workload.Process.Kill())
				_ = workload.Wait()
				require.NoError(t, cp.RemoveContainer(context.Background(), pod, ctr))
				require.Empty(t, cp.partitions.leases)
			})
		}
	}
}

func writePartitionFile(t *testing.T, path, name, value string) {
	t.Helper()
	dir, err := os.OpenRoot(path)
	require.NoError(t, err)
	defer dir.Close()
	require.NoError(t, partitionWrite(dir, name, value))
}

func TestPartitionDRAAndNRIHooks(t *testing.T) {
	cp, _ := newMetricsTestDriverWithCPUInfos(t, mockCPUInfos_SingleSocket_4CPUS_HT)
	root := t.TempDir()
	var err error
	cp.partitions, err = newCPUSetPartitions(root, "root")
	require.NoError(t, err)
	t.Cleanup(func() {
		for id := range cp.partitions.leases {
			require.NoError(t, cp.partitions.release(id))
		}
	})
	_, err = cp.Synchronize(t.Context(), nil, nil)
	require.NoError(t, err)
	claim := individualMetricsClaim("poc", "cpudev1")
	prepared, err := cp.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.NoError(t, prepared[claim.UID].Err)
	pod := &api.PodSandbox{Id: "sandbox", Uid: "pod", Name: "pod"}
	ctr := &api.Container{Id: "first", Name: "app", PodSandboxId: pod.Id, Env: []string{"DRA_CPUSET_poc=1"}, Linux: &api.LinuxContainer{CgroupsPath: "pool/pod/ctr"}}
	fakePartitionTree(t, root, "pool/pod/ctr", cpuset.New(1))
	_, _, err = cp.CreateContainer(t.Context(), pod, ctr)
	require.NoError(t, err)
	require.NoError(t, cp.StartContainer(t.Context(), pod, ctr))
	ctr.State = api.ContainerState_CONTAINER_RUNNING
	_, err = cp.Synchronize(t.Context(), []*api.PodSandbox{pod}, []*api.Container{ctr})
	require.NoError(t, err)
	writePartitionFile(t, filepath.Join(root, "pool/pod/ctr"), "cpuset.cpus.partition", "root invalid (test)")
	_, err = cp.Synchronize(t.Context(), []*api.PodSandbox{pod}, []*api.Container{ctr})
	require.Error(t, err)
	require.True(t, cp.podConfigStore.GetContainerState("pod", "app").MatchesContainer("app", "first"))
	_, err = cp.StopContainer(t.Context(), pod, ctr)
	require.NoError(t, err)
	ctr.Id = "replacement"
	_, _, err = cp.CreateContainer(t.Context(), pod, ctr)
	require.NoError(t, err)
	require.NoError(t, cp.StartContainer(t.Context(), pod, ctr))
	_, err = cp.StopContainer(t.Context(), pod, ctr)
	require.NoError(t, err)
	require.NoError(t, cp.RemoveContainer(t.Context(), pod, ctr))
	released, err := cp.UnprepareResourceClaims(t.Context(), []kubeletplugin.NamespacedObject{{UID: claim.UID}})
	require.NoError(t, err)
	require.NoError(t, released[claim.UID])
	require.Empty(t, cp.partitions.nodes)
	_, ok := cp.cpuAllocationStore.GetResourceClaimAllocation(claim.UID)
	require.False(t, ok)
}

func TestPartitionReleaseRetainsJournalOnConflict(t *testing.T) {
	root, cp, pod, ctr := partitionFixture(t)
	require.NoError(t, cp.StartContainer(t.Context(), pod, ctr))
	writePartitionFile(t, filepath.Join(root, "pool"), "cpuset.cpus.exclusive", "1-2")
	_, err := cp.StopContainer(t.Context(), pod, ctr)
	require.ErrorContains(t, err, "foreign exclusive mask")
	require.NotNil(t, cp.partitions.leases[ctr.Id])
	require.NotNil(t, cp.podConfigStore.GetContainerState(types.UID(pod.Uid), ctr.Name))
	require.Equal(t, "1-2", readPartitionFile(t, root, "pool", "cpuset.cpus.exclusive"))
	writePartitionFile(t, filepath.Join(root, "pool"), "cpuset.cpus.exclusive", "1")
	_, err = cp.StopContainer(t.Context(), pod, ctr)
	require.NoError(t, err)
	require.Empty(t, cp.partitions.leases)
	require.Empty(t, cp.partitions.nodes)
}

func TestPartitionOptions(t *testing.T) {
	p, err := newCPUSetPartitions("", "")
	require.NoError(t, err)
	require.Nil(t, p)
	for _, mode := range []string{"member", "auto", "ROOT"} {
		_, err = newCPUSetPartitions("/sys/fs/cgroup", mode)
		require.Error(t, err)
	}
	_, err = newCPUSetPartitions("relative", "root")
	require.Error(t, err)
}

// Like the runtime init at StartContainer, this task is already in its cgroup
// but has not begun its workload. The test releases it only after verification.
func pausedPartitionTask(t *testing.T, group string) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sh", "-c", "kill -STOP $$; exec sleep 300")
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	require.Eventually(t, func() bool {
		return strings.Contains(readPartitionFile(t, "/proc", fmt.Sprint(cmd.Process.Pid), "status"), "State:\tT")
	}, time.Second, time.Millisecond)
	writePartitionFile(t, group, "cgroup.procs", fmt.Sprint(cmd.Process.Pid))
	return cmd
}

func startPartitionTask(t *testing.T, cp *CPUDriver, pod *api.PodSandbox, ctr *api.Container, group string, cpus cpuset.CPUSet) *exec.Cmd {
	t.Helper()
	writePartitionFile(t, group, "cpuset.cpus", cpus.String())
	task := pausedPartitionTask(t, group)
	require.NoError(t, cp.StartContainer(t.Context(), pod, ctr))
	require.Contains(t, readPartitionFile(t, "/proc", fmt.Sprint(task.Process.Pid), "status"), "Cpus_allowed_list:\t"+cpus.String()+"\n")
	require.NoError(t, task.Process.Signal(syscall.SIGCONT))
	return task
}

func TestPartitionNodeChecks(t *testing.T) {
	for _, tc := range []struct {
		name, file, value string
		want              cpuset.CPUSet
		exact             bool
		wantError         bool
	}{
		{"ancestor union", "cpuset.cpus.exclusive.effective", "1-2", cpuset.New(1), false, false},
		{"leaf extra CPU", "cpuset.cpus.exclusive.effective", "1-2", cpuset.New(1), true, true},
		{"missing grant", "cpuset.cpus.exclusive.effective", "2", cpuset.New(1), false, true},
		{"malformed", "cpuset.cpus.effective", "broken", cpuset.New(1), true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, tc.file), []byte(tc.value), 0600))
			dir, err := os.OpenRoot(root)
			require.NoError(t, err)
			defer dir.Close()
			node := &partitionNode{dir: dir}
			err = node.checkCPUs(tc.file, tc.want, tc.exact)
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
