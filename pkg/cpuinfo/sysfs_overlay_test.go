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

package cpuinfo

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/sysfs"
)

func TestSystemCPUInfoUsesSysFSOverlay(t *testing.T) {
	tmpDir := t.TempDir()
	createFakeCPUTopology(t, tmpDir, fakeCPUTopology{
		numSockets:            1,
		numNumaNodesPerSocket: 1,
		numCoresPerNumaNode:   2,
		cpusPerCore:           1,
		coresPerL3:            2,
	})

	base := os.DirFS(filepath.Join(tmpDir, "sys")).(sysfs.FS)
	overlayFS, err := sysfs.NewOverlayFromYAML(base, []byte(`
/sys/devices/system/cpu/online: "0-1\n"
/sys/devices/system/cpu/cpu0/topology/physical_package_id: "7\n"
/sys/devices/system/node/node0/cpulist: "0\n"
/sys/devices/system/node/node1/cpulist: "1\n"
/sys/devices/system/cpu/cpu1/node1: ""
`))
	if err != nil {
		t.Fatalf("NewOverlayFromYAML() error = %v", err)
	}

	provider := NewSystemCPUInfo(overlayFS)
	infos, err := provider.GetCPUInfos(testr.New(t))
	if err != nil {
		t.Fatalf("GetCPUInfos() error = %v", err)
	}
	if got, want := len(infos), 2; got != want {
		t.Fatalf("GetCPUInfos() returned %d CPUs, want %d", got, want)
	}
	if got, want := infos[0].SocketID, 7; got != want {
		t.Fatalf("SocketID = %d, want %d", got, want)
	}
	if got, want := infos[1].NUMANodeID, 1; got != want {
		t.Fatalf("CPU 1 NUMANodeID = %d, want %d", got, want)
	}
	if got, want := infos[1].NumaNodeCPUSet.String(), "1"; got != want {
		t.Fatalf("CPU 1 NumaNodeCPUSet = %q, want %q", got, want)
	}
}

func TestSystemCPUInfoFromCompleteSysFSOverlay(t *testing.T) {
	overlayFS, err := sysfs.NewOverlayFromYAML(fstest.MapFS{}, []byte(`
/sys/devices/system/cpu/online: "999\n"
/sys/devices/system/cpu/smt/control: "on\n"
/sys/devices/system/node/node5/cpulist: "998-999\n"
/sys/devices/system/cpu/cpu999/node5: ""
/sys/devices/system/cpu/cpu999/topology/physical_package_id: "7\n"
/sys/devices/system/cpu/cpu999/topology/core_id: "3\n"
/sys/devices/system/cpu/cpu999/topology/cluster_id: "4\n"
/sys/devices/system/cpu/cpu999/topology/thread_siblings_list: "999\n"
/sys/devices/system/cpu/cpu999/cache/index3/level: "3\n"
/sys/devices/system/cpu/cpu999/cache/index3/id: "11\n"
/sys/devices/system/cpu/cpu999/cache/index3/shared_cpu_list: "999\n"
`))
	if err != nil {
		t.Fatalf("NewOverlayFromYAML() error = %v", err)
	}

	topology, err := NewSystemCPUInfo(overlayFS).GetCPUTopology(testr.New(t))
	if err != nil {
		t.Fatalf("GetCPUTopology() error = %v", err)
	}

	if got, want := topology.NumCPUs, 1; got != want {
		t.Fatalf("NumCPUs = %d, want %d", got, want)
	}
	cpu, ok := topology.CPUDetails[999]
	if !ok {
		t.Fatal("CPUDetails does not contain overlaid CPU 999")
	}
	if got, want := cpu.SocketID, 7; got != want {
		t.Errorf("SocketID = %d, want %d", got, want)
	}
	if got, want := cpu.CoreID, 3; got != want {
		t.Errorf("CoreID = %d, want %d", got, want)
	}
	if got, want := cpu.ClusterID, 4; got != want {
		t.Errorf("ClusterID = %d, want %d", got, want)
	}
	if got, want := cpu.NUMANodeID, 5; got != want {
		t.Errorf("NUMANodeID = %d, want %d", got, want)
	}
	if got, want := cpu.NumaNodeCPUSet.String(), "998-999"; got != want {
		t.Errorf("NumaNodeCPUSet = %q, want %q", got, want)
	}
	if got, want := cpu.UncoreCacheID, 11; got != want {
		t.Errorf("UncoreCacheID = %d, want %d", got, want)
	}
	if !topology.SMTEnabled {
		t.Error("SMTEnabled = false, want true")
	}
}

func TestSystemCPUInfoUsesHostRootAsOverlayBase(t *testing.T) {
	hostRoot := t.TempDir()
	t.Setenv("HOST_ROOT", hostRoot)

	writeFakeSysFSFile := func(name, contents string) {
		t.Helper()

		filename := filepath.Join(hostRoot, "sys", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(filename), 0o755); err != nil {
			t.Fatalf("create parent directory for %q: %v", filename, err)
		}
		if err := os.WriteFile(filename, []byte(contents), 0o600); err != nil {
			t.Fatalf("write %q: %v", filename, err)
		}
	}

	writeFakeSysFSFile("devices/system/cpu/online", "42\n")
	writeFakeSysFSFile("devices/system/cpu/smt/control", "off\n")
	writeFakeSysFSFile("devices/system/node/node5/cpulist", "42\n")
	writeFakeSysFSFile("devices/system/cpu/cpu42/node5", "")
	writeFakeSysFSFile("devices/system/cpu/cpu42/topology/physical_package_id", "1\n")
	writeFakeSysFSFile("devices/system/cpu/cpu42/topology/core_id", "3\n")
	writeFakeSysFSFile("devices/system/cpu/cpu42/topology/cluster_id", "4\n")
	writeFakeSysFSFile("devices/system/cpu/cpu42/topology/thread_siblings_list", "42\n")
	writeFakeSysFSFile("devices/system/cpu/cpu42/cache/index3/level", "3\n")
	writeFakeSysFSFile("devices/system/cpu/cpu42/cache/index3/id", "11\n")
	writeFakeSysFSFile("devices/system/cpu/cpu42/cache/index3/shared_cpu_list", "42\n")

	overlayFS, err := sysfs.NewOverlayFromYAML(sysfs.Host(), []byte(`
/sys/devices/system/cpu/smt/control: "on\n"
/sys/devices/system/cpu/cpu42/topology/physical_package_id: "7\n"
`))
	if err != nil {
		t.Fatalf("NewOverlayFromYAML() error = %v", err)
	}

	topology, err := NewSystemCPUInfo(overlayFS).GetCPUTopology(testr.New(t))
	if err != nil {
		t.Fatalf("GetCPUTopology() error = %v", err)
	}

	if got, want := topology.NumCPUs, 1; got != want {
		t.Fatalf("NumCPUs = %d, want %d", got, want)
	}
	if !topology.SMTEnabled {
		t.Error("SMTEnabled = false, want true from overlay")
	}

	cpu, ok := topology.CPUDetails[42]
	if !ok {
		t.Fatal("CPUDetails does not contain HOST_ROOT CPU 42")
	}
	if got, want := cpu.SocketID, 7; got != want {
		t.Errorf("SocketID = %d, want %d", got, want)
	}
	if got, want := cpu.CoreID, 3; got != want {
		t.Errorf("CoreID = %d, want %d", got, want)
	}
	if got, want := cpu.ClusterID, 4; got != want {
		t.Errorf("ClusterID = %d, want %d", got, want)
	}
	if got, want := cpu.NUMANodeID, 5; got != want {
		t.Errorf("NUMANodeID = %d, want %d", got, want)
	}
	if got, want := cpu.NumaNodeCPUSet.String(), "42"; got != want {
		t.Errorf("NumaNodeCPUSet = %q, want %q", got, want)
	}
	if got, want := cpu.UncoreCacheID, 11; got != want {
		t.Errorf("UncoreCacheID = %d, want %d", got, want)
	}
}
