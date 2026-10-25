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

package cpupartition

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/utils/cpuset"
)

// TestKernel runs against a real cgroup v2 hierarchy. It only creates and
// removes its own subtree. Run it as root on a disposable host:
//
//	go test -c -o cpupartition.test ./pkg/cpupartition
//	sudo DRACPU_PARTITION_TEST_ROOT=/sys/fs/cgroup ./cpupartition.test -test.run TestKernel -test.v
//
// DRACPU_PARTITION_TEST_CPUS selects the CPUs; the default is the last one.
func TestKernel(t *testing.T) {
	root := os.Getenv("DRACPU_PARTITION_TEST_ROOT")
	if root == "" {
		t.Skip("set DRACPU_PARTITION_TEST_ROOT on a disposable cgroup v2 host")
	}
	require.Equal(t, 0, os.Geteuid())
	all, err := cpuset.Parse(get(t, root, "", fileEffective))
	require.NoError(t, err)
	require.GreaterOrEqual(t, all.Size(), 2)
	cpus := cpuset.New(all.List()[all.Size()-1])
	if v := os.Getenv("DRACPU_PARTITION_TEST_CPUS"); v != "" {
		cpus, err = cpuset.Parse(v)
		require.NoError(t, err)
		require.True(t, cpus.IsSubsetOf(all) && cpus.Size() < all.Size())
	}
	for _, mode := range []string{ModeRoot, ModeIsolated} {
		// kubelet adds a QoS slice above the pod for burstable and best effort.
		for name, qos := range map[string]string{"guaranteed": "", "burstable": "burstable/", "besteffort": "besteffort/"} {
			t.Run(mode+"/"+name, func(t *testing.T) {
				kernelScenario(t, root, mode, qos, all, cpus)
			})
		}
	}
}

func kernelScenario(t *testing.T, root, mode, qos string, all, cpus cpuset.CPUSet) {
	base, err := os.MkdirTemp(root, "dra330-")
	require.NoError(t, err)
	t.Cleanup(func() { removeCgroup(t, base) })
	scope := filepath.Base(base)
	mkdir := func(dir string) string {
		t.Helper()
		parts := strings.Split(dir, "/")
		for i := range parts {
			p := filepath.Join(base, filepath.Join(parts[:i+1]...))
			if _, err := os.Stat(p); err == nil { //nolint:gosec // Below the test's own cgroup.
				continue
			}
			require.NoError(t, os.Mkdir(p, 0750)) //nolint:gosec // Below the test's own cgroup.
			t.Cleanup(func() { removeCgroup(t, p) })
		}
		return scope + "/" + dir
	}
	newManager := func() *Manager {
		m, err := New(Config{Mode: mode, CgroupRoot: root, Scope: scope})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, m.Close()) })
		return m
	}

	set(t, root, scope, "cgroup.subtree_control", "+cpuset")
	pod := mkdir(qos + "pod")
	for _, d := range chain(strings.TrimPrefix(pod, scope+"/")) {
		set(t, root, scope+"/"+d, "cgroup.subtree_control", "+cpuset")
	}
	leaf := mkdir(qos + "pod/ctr")
	sidecar := mkdir(qos + "pod/sidecar")
	set(t, root, sidecar, fileCPUs, all.Difference(cpus).String())
	other := runIn(t, root, mkdir("other"), false)
	set(t, root, leaf, fileCPUs, cpus.String())

	// The workload is held stopped, as between task creation and start, and
	// must already be confined when Activate returns.
	m := newManager()
	task := runIn(t, root, leaf, true)
	require.NoError(t, m.Activate("ctr", "/"+leaf, cpus))
	require.Equal(t, cpus.String(), allowedCPUs(t, task))
	require.Equal(t, all.Difference(cpus).String(), allowedCPUs(t, other))
	require.NoError(t, task.Process.Signal(syscall.SIGCONT))

	// A new manager adopts the partition from the kernel.
	m2 := newManager()
	st, err := m2.Reconcile(map[string]Lease{"ctr": {CgroupsPath: "/" + leaf, CPUs: cpus}})
	require.NoError(t, err)
	require.Equal(t, 1, st.Valid, st.Errors)
	require.NoError(t, st.Cleanup)

	// Withdrawing the pod grant invalidates the partition and is notified.
	events := m2.Watch(t.Context(), time.Hour)
	set(t, root, pod, fileExclusive, "")
	require.Contains(t, get(t, root, leaf, filePartition), " invalid")
	select {
	case <-events:
	case <-time.After(5 * time.Second):
		t.Fatal("no notification for the invalidated partition")
	}
	st = repair(t, m2)
	require.Equal(t, 1, st.Repaired, st.Errors)
	require.Equal(t, mode, get(t, root, leaf, filePartition))
	require.Equal(t, cpus.String(), allowedCPUs(t, task))

	// The container and its cgroup go away while no driver runs. Its grant
	// stays on the ancestors until the next reconcile.
	require.NoError(t, task.Process.Kill())
	_ = task.Wait()
	require.NoError(t, os.Remove(filepath.Join(root, leaf))) //nolint:gosec // The test's own cgroup.
	require.Equal(t, cpus.String(), get(t, root, pod, fileExclusive))
	st, err = newManager().Reconcile(nil)
	require.NoError(t, err)
	require.NoError(t, st.Cleanup)
	for _, d := range chain(pod) {
		require.Empty(t, get(t, root, d, fileExclusive), d)
	}
	require.Equal(t, all.String(), allowedCPUs(t, other))
}

// runIn starts a task in cgroup, stopped if paused.
func runIn(t *testing.T, root, cgroup string, paused bool) *exec.Cmd {
	t.Helper()
	script := "exec sleep 300"
	if paused {
		script = "kill -STOP $$; exec sleep 300"
	}
	cmd := exec.Command("sh", "-c", script)
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	if paused {
		require.Eventually(t, func() bool {
			return strings.Contains(get(t, "/proc", fmt.Sprint(cmd.Process.Pid), "status"), "State:\tT")
		}, time.Second, time.Millisecond)
	}
	set(t, root, cgroup, "cgroup.procs", fmt.Sprint(cmd.Process.Pid))
	return cmd
}

func allowedCPUs(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	for line := range strings.Lines(get(t, "/proc", fmt.Sprint(cmd.Process.Pid), "status")) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "Cpus_allowed_list:"); ok {
			return strings.TrimSpace(v)
		}
	}
	t.Fatal("no Cpus_allowed_list")
	return ""
}

// removeCgroup clears partition state before removing a test cgroup, so a
// failed assertion leaves nothing behind on the host.
//
//nolint:gosec // Only cgroups this test created.
func removeCgroup(t *testing.T, dir string) {
	if _, err := os.Stat(dir); err != nil {
		return
	}
	for _, f := range [][2]string{{filePartition, modeMember}, {fileExclusive, ""}} {
		if _, err := os.Stat(filepath.Join(dir, f[0])); err == nil {
			_ = os.WriteFile(filepath.Join(dir, f[0]), []byte(f[1]+"\n"), 0)
		}
	}
	for range 50 {
		if err := os.Remove(dir); err == nil || os.IsNotExist(err) {
			return
		}
		time.Sleep(20 * time.Millisecond) // killed tasks leave asynchronously
	}
	t.Errorf("cgroup %s left behind", dir)
}
