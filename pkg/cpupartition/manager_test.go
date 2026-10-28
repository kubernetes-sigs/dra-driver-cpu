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
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpupartition/cgrouptest"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/cpuset"
)

var (
	create = cgrouptest.Create
	set    = cgrouptest.Write
	get    = cgrouptest.Read
)

func newManager(t *testing.T, mode string) (*Manager, string) {
	t.Helper()
	root := t.TempDir()
	m, err := New(Config{Mode: mode, CgroupRoot: root, Scope: "pods"})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	return m, root
}

// failWrites fails writes to files whose path contains match, until the
// returned func restores writeFile.
func failWrites(t *testing.T, match string) func() {
	t.Helper()
	orig := writeFile
	restore := func() { writeFile = orig }
	writeFile = func(root *os.Root, name, value string) error {
		if strings.Contains(name, match) {
			return errors.New("injected write failure")
		}
		return orig(root, name, value)
	}
	t.Cleanup(restore)
	return restore
}

func repair(t *testing.T, m *Manager) Status {
	t.Helper()
	st, err := m.Repair()
	require.NoError(t, err)
	return st
}

func TestConfigValidate(t *testing.T) {
	require.NoError(t, Config{}.Validate())
	require.NoError(t, Config{Mode: ModeIsolated, CgroupRoot: "/sys/fs/cgroup", Scope: "kubepods.slice"}.Validate())
	for _, c := range []Config{
		{Mode: "member", CgroupRoot: "/sys/fs/cgroup", Scope: "kubepods.slice"},
		{Mode: ModeRoot, CgroupRoot: "relative", Scope: "kubepods.slice"},
		{Mode: ModeRoot, CgroupRoot: "/sys/fs/cgroup", Scope: "."},
		{Mode: ModeRoot, CgroupRoot: "/sys/fs/cgroup", Scope: "../x"},
		{Mode: ModeRoot, CgroupRoot: "/sys/fs/cgroup", Scope: "/kubepods.slice"},
	} {
		require.Error(t, c.Validate(), c)
	}
	_, err := New(Config{})
	require.Error(t, err)
}

func TestCgroupPath(t *testing.T) {
	for raw, want := range map[string]string{
		"/kubepods/pod/ctr": "kubepods/pod/ctr",
		"kubepods-burstable-pod123.slice:cri-containerd:abc": "kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod123.slice/cri-containerd-abc.scope",
		"kubepods-pod123.slice:crio:abc":                     "kubepods.slice/kubepods-pod123.slice/crio-abc.scope",
	} {
		got, err := CgroupPath(raw)
		require.NoError(t, err)
		require.Equal(t, want, got)
	}
	for _, raw := range []string{"", "/", "../escape", "/a/../b", "/a/./b", "/a//b", "bad:prefix:id", "a.slice:../x:id", "a.slice:x:../id", "-.slice:x:id"} {
		_, err := CgroupPath(raw)
		require.Error(t, err, raw)
	}
}

func TestActivateAndRelease(t *testing.T) {
	m, root := newManager(t, ModeRoot)
	create(t, root, "pods/pod/one", "1")
	create(t, root, "pods/pod/two", "2")
	require.NoError(t, m.Activate("one", "/pods/pod/one", cpuset.New(1)))
	require.NoError(t, m.Activate("one", "/pods/pod/one", cpuset.New(1)))
	require.NoError(t, m.Activate("two", "/pods/pod/two", cpuset.New(2)))
	require.Equal(t, "1-2", get(t, root, "pods", fileExclusive))
	require.Equal(t, "1-2", get(t, root, "pods/pod", fileExclusive))
	require.Equal(t, ModeRoot, get(t, root, "pods/pod/one", filePartition))
	require.Equal(t, []string{"one"}, m.Holding(cpuset.New(1)))

	require.NoError(t, m.Release("one"))
	require.NoError(t, m.Release("one"))
	require.Equal(t, "2", get(t, root, "pods/pod", fileExclusive))
	require.Equal(t, modeMember, get(t, root, "pods/pod/one", filePartition))
	require.Empty(t, get(t, root, "pods/pod/one", fileExclusive))
	require.Empty(t, m.Holding(cpuset.New(1)))

	// The runtime may remove the cgroup first.
	require.NoError(t, os.RemoveAll(filepath.Join(root, "pods/pod/two")))
	require.NoError(t, m.Release("two"))
	require.Empty(t, get(t, root, "pods", fileExclusive))
	require.Empty(t, m.leases)
	require.Empty(t, m.pending)
}

func TestActivateRejectsAndRollsBack(t *testing.T) {
	for name, tc := range map[string]struct {
		setup func(t *testing.T, root string)
		path  string
		want  string
	}{
		"runtime mask": {func(t *testing.T, root string) { set(t, root, "pods/pod/ctr", fileCPUs, "2") }, "/pods/pod/ctr", "runtime cpuset"},
		"invalid":      {func(t *testing.T, root string) { set(t, root, "pods/pod/ctr", fileEffective, "1-2") }, "/pods/pod/ctr", fileEffective},
		"no controller": {func(t *testing.T, root string) {
			require.NoError(t, os.Remove(filepath.Join(root, "pods/pod", fileExclusive)))
		}, "/pods/pod/ctr", "cpuset controller"},
		"out of scope": {func(*testing.T, string) {}, "/other/ctr", "outside scope"},
		"scope itself": {func(*testing.T, string) {}, "/pods", "outside scope"},
		"symlink": {func(t *testing.T, root string) {
			require.NoError(t, os.Rename(filepath.Join(root, "pods/pod"), filepath.Join(t.TempDir(), "pod")))
			require.NoError(t, os.Symlink(t.TempDir(), filepath.Join(root, "pods/pod")))
		}, "/pods/pod/ctr", "path escapes"},
	} {
		t.Run(name, func(t *testing.T) {
			m, root := newManager(t, ModeIsolated)
			create(t, root, "pods/pod/ctr", "1")
			tc.setup(t, root)
			require.ErrorContains(t, m.Activate("ctr", tc.path, cpuset.New(1)), tc.want)
			require.Empty(t, m.leases)
			if name == "symlink" {
				// The escaped cgroup cannot be shown clean, so it keeps the CPUs.
				require.Equal(t, []string{"ctr"}, m.Holding(cpuset.New(1)))
			} else {
				require.Empty(t, m.pending)
			}
			require.Empty(t, get(t, root, "pods", fileExclusive))
		})
	}
}

func TestActivateConflicts(t *testing.T) {
	m, root := newManager(t, ModeRoot)
	create(t, root, "pods/a/ctr", "1-2")
	create(t, root, "pods/b/ctr", "2")
	require.NoError(t, m.Activate("a", "/pods/a/ctr", cpuset.New(1, 2)))
	require.ErrorContains(t, m.Activate("b", "/pods/b/ctr", cpuset.New(2)), "overlap")
	require.ErrorContains(t, m.Activate("a", "/pods/b/ctr", cpuset.New(3)), "different partition")
	require.ErrorContains(t, m.Activate("c", "/pods/a/ctr/nested", cpuset.New(3)), "nests")
	require.Equal(t, "1-2", get(t, root, "pods", fileExclusive))
}

func TestIncompleteReleaseHoldsCPUs(t *testing.T) {
	m, root := newManager(t, ModeRoot)
	create(t, root, "pods/pod/ctr", "1")
	create(t, root, "pods/pod/next", "1")
	require.NoError(t, m.Activate("ctr", "/pods/pod/ctr", cpuset.New(1)))

	restore := failWrites(t, "pods/pod/"+fileExclusive)
	require.ErrorContains(t, m.Release("ctr"), "injected")
	require.Equal(t, []string{"ctr"}, m.Holding(cpuset.New(1)))
	require.ErrorContains(t, m.Activate("next", "/pods/pod/next", cpuset.New(1)), "overlap")
	st := repair(t, m)
	require.Equal(t, 1, st.Releasing)
	require.ErrorContains(t, st.Errors["ctr"], "injected")

	restore()
	require.NoError(t, m.Activate("next", "/pods/pod/next", cpuset.New(1)))
	require.Empty(t, m.pending)
	require.Equal(t, "1", get(t, root, "pods/pod", fileExclusive))
	require.Equal(t, modeMember, get(t, root, "pods/pod/ctr", filePartition))
}

func TestReconcileRecoversWithoutCheckpoint(t *testing.T) {
	m, root := newManager(t, ModeRoot)
	// a: valid partition left by the previous driver; adopt it.
	create(t, root, "pods/a/ctr", "1")
	set(t, root, "pods/a", fileExclusive, "1")
	set(t, root, "pods/a/ctr", fileExclusive, "1")
	set(t, root, "pods/a/ctr", filePartition, ModeRoot)
	// gone: grant of a container removed while the driver was down.
	create(t, root, "pods/gone", "")
	set(t, root, "pods/gone", fileExclusive, "5")
	// c: partition of a container that is no longer trusted.
	create(t, root, "pods/c/ctr", "3")
	set(t, root, "pods/c", fileExclusive, "3")
	set(t, root, "pods/c/ctr", fileExclusive, "3")
	set(t, root, "pods/c/ctr", filePartition, ModeRoot+" invalid (stale)")
	set(t, root, "pods", fileExclusive, "1,3,5")
	// b: started while the driver was down; d: conflicts with b.
	create(t, root, "pods/b/ctr", "2")
	create(t, root, "pods/d/ctr", "2")

	st, err := m.Reconcile(map[string]Lease{
		"a": {CgroupsPath: "/pods/a/ctr", CPUs: cpuset.New(1)},
		"b": {CgroupsPath: "/pods/b/ctr", CPUs: cpuset.New(2)},
		"d": {CgroupsPath: "/pods/d/ctr", CPUs: cpuset.New(2)},
	})
	require.NoError(t, err)
	require.NoError(t, st.Cleanup)
	require.Equal(t, 2, st.Valid)
	require.Equal(t, 1, st.Invalid)
	require.ErrorContains(t, st.Errors["d"], "overlap")
	require.Equal(t, "1-2", get(t, root, "pods", fileExclusive))
	require.Empty(t, get(t, root, "pods/gone", fileExclusive))
	require.Empty(t, get(t, root, "pods/c", fileExclusive))
	require.Equal(t, modeMember, get(t, root, "pods/c/ctr", filePartition))
	require.Equal(t, ModeRoot, get(t, root, "pods/b/ctr", filePartition))

	// On reconnect a vanished container is released.
	st, err = m.Reconcile(map[string]Lease{"b": {CgroupsPath: "/pods/b/ctr", CPUs: cpuset.New(2)}})
	require.NoError(t, err)
	require.Equal(t, 1, st.Valid)
	require.Equal(t, "2", get(t, root, "pods", fileExclusive))
	require.Equal(t, modeMember, get(t, root, "pods/a/ctr", filePartition))
}

func TestReconcileScanFailureWritesNothing(t *testing.T) {
	m, root := newManager(t, ModeRoot)
	create(t, root, "pods/a", "")
	set(t, root, "pods/a", fileExclusive, "not-a-cpuset")
	set(t, root, "pods", fileExclusive, "4")
	_, err := m.Reconcile(nil)
	require.ErrorContains(t, err, "scan")
	require.Equal(t, "4", get(t, root, "pods", fileExclusive))
}

func TestRepairRestoresInvalidPartition(t *testing.T) {
	m, root := newManager(t, ModeIsolated)
	create(t, root, "pods/pod/ctr", "1")
	require.NoError(t, m.Activate("ctr", "/pods/pod/ctr", cpuset.New(1)))
	require.Equal(t, Status{Valid: 1, Errors: map[string]error{}}, repair(t, m))

	set(t, root, "pods/pod", fileExclusive, "")
	set(t, root, "pods/pod/ctr", filePartition, ModeIsolated+" invalid (Parent is not a partition root)")
	st := repair(t, m)
	require.Equal(t, 1, st.Repaired)
	require.Equal(t, 1, st.Valid)
	require.Equal(t, "1", get(t, root, "pods/pod", fileExclusive))
	require.Equal(t, ModeIsolated, get(t, root, "pods/pod/ctr", filePartition))

	set(t, root, "pods/pod/ctr", fileEffective, "")
	st = repair(t, m)
	require.Equal(t, 1, st.Invalid)
	require.ErrorContains(t, st.Errors["ctr"], fileEffective)
}

func TestWatchSignalsAndStops(t *testing.T) {
	m, _ := newManager(t, ModeRoot)
	events := m.Watch(t.Context(), 10*time.Millisecond)
	select {
	case <-events:
	case <-time.After(time.Second):
		t.Fatal("no periodic signal")
	}
	require.NoError(t, m.Close())
	for range events { //nolint:revive // Drain until Close stops the watch.
	}
}
