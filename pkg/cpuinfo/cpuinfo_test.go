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

package cpuinfo

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"k8s.io/utils/cpuset"
)

func TestParseCPUInfo(t *testing.T) {
	lines := []string{
		"processor\t: 0",
		"physical id\t: 0",
		"core id\t\t: 0",
	}
	eCoreCpus := cpuset.New()
	cpuInfo := parseCPUInfo(false, eCoreCpus, lines...)
	if cpuInfo == nil {
		t.Fatal("parseCPUInfo returned nil")
	}
	if cpuInfo.CoreType != CoreTypeStandard {
		t.Errorf("expected CoreTypeStandard, got %v", cpuInfo.CoreType)
	}

	eCoreCpusWithCpu0, _ := cpuset.Parse("0")
	cpuInfo = parseCPUInfo(true, eCoreCpusWithCpu0, lines...)
	if cpuInfo.CoreType != CoreTypeEfficiency {
		t.Errorf("expected CoreTypeEfficiency, got %v", cpuInfo.CoreType)
	}
}

func TestPopulateCpuSiblings(t *testing.T) {
	testCases := []struct {
		name             string
		input            []CPUInfo
		expectedSiblings map[int]int
	}{
		{
			name: "2-way hyper-threading",
			input: []CPUInfo{
				{CpuID: 0, SocketID: 0, CoreID: 0, SiblingCpuID: -1},
				{CpuID: 1, SocketID: 0, CoreID: 1, SiblingCpuID: -1},
				{CpuID: 2, SocketID: 0, CoreID: 0, SiblingCpuID: -1},
				{CpuID: 3, SocketID: 0, CoreID: 1, SiblingCpuID: -1},
			},
			expectedSiblings: map[int]int{0: 2, 1: 3, 2: 0, 3: 1},
		},
		{
			name: "no hyper-threading",
			input: []CPUInfo{
				{CpuID: 0, SocketID: 0, CoreID: 0, SiblingCpuID: -1},
				{CpuID: 1, SocketID: 0, CoreID: 1, SiblingCpuID: -1},
			},
			expectedSiblings: map[int]int{0: -1, 1: -1},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			populateCpuSiblings(tc.input)
			infoMap := make(map[int]CPUInfo)
			for _, info := range tc.input {
				infoMap[info.CpuID] = info
			}
			for cpuID, expectedSiblingID := range tc.expectedSiblings {
				if infoMap[cpuID].SiblingCpuID != expectedSiblingID {
					t.Errorf("cpu %d: expected sibling %d, got %d", cpuID, expectedSiblingID, infoMap[cpuID].SiblingCpuID)
				}
			}
		})
	}
}

func TestFormatAffinityMask(t *testing.T) {
	testCases := []struct {
		name     string
		mask     string
		expected string
	}{
		{
			name:     "single word",
			mask:     "0000000f",
			expected: "0xf",
		},
		{
			name:     "two words from kernel",
			mask:     "00000001,0000000f",
			expected: "0xf00000001",
		},
		{
			name:     "four words from kernel",
			mask:     "ffff0000,0000ffff,00ff00ff,ff00ff00",
			expected: "0xff00ff0000ff00ff0000ffffffff0000",
		},
		{
			name:     "empty mask",
			mask:     "",
			expected: "0x0",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result := formatAffinityMask(tc.mask)
			if result != tc.expected {
				t.Errorf("expected %s, got %s", tc.expected, result)
			}
		})
	}
}

type fakeCPUTopology struct {
	numSockets            int
	numNumaNodesPerSocket int
	numCoresPerNumaNode   int
	cpusPerCore           int
	coresPerL3            int
	hybrid                bool
	eCores                string
}

func createFakeCPUTopology(t *testing.T, dir string, topo fakeCPUTopology) {
	coresPerSocket := topo.numNumaNodesPerSocket * topo.numCoresPerNumaNode
	numCPUs := topo.numSockets * coresPerSocket * topo.cpusPerCore

	// /proc/cpuinfo
	procDir := filepath.Join(dir, "proc")
	if err := os.Mkdir(procDir, 0755); err != nil {
		t.Fatal(err)
	}
	var cpuinfoContent strings.Builder
	for i := 0; i < numCPUs; i++ {
		socketID := i / (coresPerSocket * topo.cpusPerCore)
		coreID := i % coresPerSocket
		cpuinfoContent.WriteString(fmt.Sprintf("processor\t: %d\n", i))
		cpuinfoContent.WriteString(fmt.Sprintf("physical id\t: %d\n", socketID))
		cpuinfoContent.WriteString(fmt.Sprintf("core id\t\t: %d\n", coreID))
		cpuinfoContent.WriteString("\n")
	}
	if err := os.WriteFile(filepath.Join(procDir, "cpuinfo"), []byte(cpuinfoContent.String()), 0600); err != nil {
		t.Fatal(err)
	}

	// /sys
	sysDevicesDir := filepath.Join(dir, "sys/devices")
	if topo.hybrid {
		// e-cores
		if err := os.MkdirAll(filepath.Join(sysDevicesDir, "cpu_atom"), 0755); err != nil {
			t.Fatal(err)
		}
		eCoresSet, err := cpuset.Parse(topo.eCores)
		if err != nil {
			t.Fatalf("failed to parse eCores %q: %v", topo.eCores, err)
		}
		if err := os.WriteFile(filepath.Join(sysDevicesDir, "cpu_atom/cpus"), []byte(eCoresSet.String()), 0600); err != nil {
			t.Fatal(err)
		}
	}

	// cpu topology
	cpuSysDir := filepath.Join(sysDevicesDir, "system/cpu")
	if err := os.MkdirAll(cpuSysDir, 0755); err != nil {
		t.Fatal(err)
	}
	nodeToCpus := make(map[int][]int)
	for i := 0; i < numCPUs; i++ {
		cpuDir := filepath.Join(cpuSysDir, fmt.Sprintf("cpu%d", i))
		if err := os.Mkdir(cpuDir, 0755); err != nil {
			t.Fatal(err)
		}

		// topology
		topologyDir := filepath.Join(cpuDir, "topology")
		if err := os.Mkdir(topologyDir, 0755); err != nil {
			t.Fatal(err)
		}
		socketID := i / (coresPerSocket * topo.cpusPerCore)
		if err := os.WriteFile(filepath.Join(topologyDir, "physical_package_id"), []byte(fmt.Sprintf("%d\n", socketID)), 0600); err != nil {
			t.Fatal(err)
		}

		// node
		cpusPerNumaNode := topo.numCoresPerNumaNode * topo.cpusPerCore
		nodeID := i / cpusPerNumaNode
		nodeToCpus[nodeID] = append(nodeToCpus[nodeID], i)
		nodeDir := filepath.Join(cpuDir, fmt.Sprintf("node%d", nodeID))
		if err := os.Mkdir(nodeDir, 0755); err != nil {
			t.Fatal(err)
		}

		// cache
		cacheDir := filepath.Join(cpuDir, "cache")
		if err := os.Mkdir(cacheDir, 0755); err != nil {
			t.Fatal(err)
		}
		l3CacheID := i / (topo.coresPerL3 * topo.cpusPerCore)
		index3Dir := filepath.Join(cacheDir, "index3")
		if err := os.Mkdir(index3Dir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(index3Dir, "level"), []byte("3\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(index3Dir, "id"), []byte(fmt.Sprintf("%d\n", l3CacheID)), 0600); err != nil {
			t.Fatal(err)
		}
		sharedCPUListStart := l3CacheID * (topo.coresPerL3 * topo.cpusPerCore)
		sharedCPUListEnd := sharedCPUListStart + (topo.coresPerL3 * topo.cpusPerCore) - 1
		sharedCPUList := fmt.Sprintf("%d-%d", sharedCPUListStart, sharedCPUListEnd)
		if err := os.WriteFile(filepath.Join(index3Dir, "shared_cpu_list"), []byte(sharedCPUList+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	// numa nodes
	nodeSysDir := filepath.Join(sysDevicesDir, "system/node")
	if err := os.MkdirAll(nodeSysDir, 0755); err != nil {
		t.Fatal(err)
	}
	for nodeID, cpus := range nodeToCpus {
		nodeDir := filepath.Join(nodeSysDir, fmt.Sprintf("node%d", nodeID))
		if err := os.Mkdir(nodeDir, 0755); err != nil {
			t.Fatal(err)
		}
		cpusForNode := cpuset.New(cpus...)
		// Convert cpuset to a hex string and write to cpumap.
		// This simulates the kernel's behavior of storing cpumaps as comma-separated hex words.
		const wordSize = 32
		words := []string{}
		for i := 0; i < cpusForNode.Size(); i += wordSize {
			wordCpus := []int{}
			for j := i; j < i+wordSize; j++ {
				if cpusForNode.Contains(j) {
					wordCpus = append(wordCpus, j%wordSize)
				}
			}
			wordSet := cpuset.New(wordCpus...)
			mask := 0
			for _, cpu := range wordSet.List() {
				// The value of cpu is always < 32, so this conversion is safe.
				mask |= (1 << uint(cpu)) //nolint:gosec
			}
			words = append(words, fmt.Sprintf("%08x", mask))
		}

		if err := os.WriteFile(filepath.Join(nodeDir, "cpumap"), []byte(strings.Join(words, ",")+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGetCPUInfos(t *testing.T) {
	testCases := []struct {
		name          string
		topology      fakeCPUTopology
		expectedInfos []CPUInfo
	}{
		{
			name: "non-hybrid hyper-threading",
			topology: fakeCPUTopology{
				numSockets:            1,
				numNumaNodesPerSocket: 1,
				numCoresPerNumaNode:   2,
				cpusPerCore:           2,
				coresPerL3:            2,
				hybrid:                false,
			},
			expectedInfos: []CPUInfo{
				{CpuID: 0, CoreID: 0, SocketID: 0, NumaNode: 0, NumaNodeAffinityMask: "0xf", SiblingCpuID: 2, CoreType: CoreTypeStandard, L3CacheID: 0},
				{CpuID: 1, CoreID: 1, SocketID: 0, NumaNode: 0, NumaNodeAffinityMask: "0xf", SiblingCpuID: 3, CoreType: CoreTypeStandard, L3CacheID: 0},
				{CpuID: 2, CoreID: 0, SocketID: 0, NumaNode: 0, NumaNodeAffinityMask: "0xf", SiblingCpuID: 0, CoreType: CoreTypeStandard, L3CacheID: 0},
				{CpuID: 3, CoreID: 1, SocketID: 0, NumaNode: 0, NumaNodeAffinityMask: "0xf", SiblingCpuID: 1, CoreType: CoreTypeStandard, L3CacheID: 0},
			},
		},
		{
			name: "non-hybrid no hyper-threading",
			topology: fakeCPUTopology{
				numSockets:            1,
				numNumaNodesPerSocket: 1,
				numCoresPerNumaNode:   2,
				cpusPerCore:           1,
				coresPerL3:            2,
				hybrid:                false,
			},
			expectedInfos: []CPUInfo{
				{CpuID: 0, CoreID: 0, SocketID: 0, NumaNode: 0, NumaNodeAffinityMask: "0x3", SiblingCpuID: -1, CoreType: CoreTypeStandard, L3CacheID: 0},
				{CpuID: 1, CoreID: 1, SocketID: 0, NumaNode: 0, NumaNodeAffinityMask: "0x3", SiblingCpuID: -1, CoreType: CoreTypeStandard, L3CacheID: 0},
			},
		},
		{
			name: "non-hybrid two sockets, two numa nodes",
			topology: fakeCPUTopology{
				numSockets:            2,
				numNumaNodesPerSocket: 1,
				numCoresPerNumaNode:   2,
				cpusPerCore:           2,
				coresPerL3:            2,
				hybrid:                false,
			},
			expectedInfos: []CPUInfo{
				{CpuID: 0, CoreID: 0, SocketID: 0, NumaNode: 0, NumaNodeAffinityMask: "0xf", SiblingCpuID: 2, CoreType: CoreTypeStandard, L3CacheID: 0},
				{CpuID: 1, CoreID: 1, SocketID: 0, NumaNode: 0, NumaNodeAffinityMask: "0xf", SiblingCpuID: 3, CoreType: CoreTypeStandard, L3CacheID: 0},
				{CpuID: 2, CoreID: 0, SocketID: 0, NumaNode: 0, NumaNodeAffinityMask: "0xf", SiblingCpuID: 0, CoreType: CoreTypeStandard, L3CacheID: 0},
				{CpuID: 3, CoreID: 1, SocketID: 0, NumaNode: 0, NumaNodeAffinityMask: "0xf", SiblingCpuID: 1, CoreType: CoreTypeStandard, L3CacheID: 0},
				{CpuID: 4, CoreID: 0, SocketID: 1, NumaNode: 1, NumaNodeAffinityMask: "0xf0", SiblingCpuID: 6, CoreType: CoreTypeStandard, L3CacheID: 1},
				{CpuID: 5, CoreID: 1, SocketID: 1, NumaNode: 1, NumaNodeAffinityMask: "0xf0", SiblingCpuID: 7, CoreType: CoreTypeStandard, L3CacheID: 1},
				{CpuID: 6, CoreID: 0, SocketID: 1, NumaNode: 1, NumaNodeAffinityMask: "0xf0", SiblingCpuID: 4, CoreType: CoreTypeStandard, L3CacheID: 1},
				{CpuID: 7, CoreID: 1, SocketID: 1, NumaNode: 1, NumaNodeAffinityMask: "0xf0", SiblingCpuID: 5, CoreType: CoreTypeStandard, L3CacheID: 1},
			},
		},
		{
			name: "hybrid with e-cores",
			topology: fakeCPUTopology{
				numSockets:            1,
				numNumaNodesPerSocket: 1,
				numCoresPerNumaNode:   4,
				cpusPerCore:           1,
				coresPerL3:            4,
				hybrid:                true,
				eCores:                "2,3",
			},
			expectedInfos: []CPUInfo{
				{CpuID: 0, CoreID: 0, SocketID: 0, NumaNode: 0, NumaNodeAffinityMask: "0xf", SiblingCpuID: -1, CoreType: CoreTypePerformance, L3CacheID: 0},
				{CpuID: 1, CoreID: 1, SocketID: 0, NumaNode: 0, NumaNodeAffinityMask: "0xf", SiblingCpuID: -1, CoreType: CoreTypePerformance, L3CacheID: 0},
				{CpuID: 2, CoreID: 2, SocketID: 0, NumaNode: 0, NumaNodeAffinityMask: "0xf", SiblingCpuID: -1, CoreType: CoreTypeEfficiency, L3CacheID: 0},
				{CpuID: 3, CoreID: 3, SocketID: 0, NumaNode: 0, NumaNodeAffinityMask: "0xf", SiblingCpuID: -1, CoreType: CoreTypeEfficiency, L3CacheID: 0},
			},
		},
		{
			name: "hybrid with empty e-cores file",
			topology: fakeCPUTopology{
				numSockets:            1,
				numNumaNodesPerSocket: 1,
				numCoresPerNumaNode:   2,
				cpusPerCore:           1,
				coresPerL3:            2,
				hybrid:                true,
				eCores:                "",
			},
			expectedInfos: []CPUInfo{
				{CpuID: 0, CoreID: 0, SocketID: 0, NumaNode: 0, NumaNodeAffinityMask: "0x3", SiblingCpuID: -1, CoreType: CoreTypePerformance, L3CacheID: 0},
				{CpuID: 1, CoreID: 1, SocketID: 0, NumaNode: 0, NumaNodeAffinityMask: "0x3", SiblingCpuID: -1, CoreType: CoreTypePerformance, L3CacheID: 0},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			t.Setenv("HOST_ROOT", tmpDir)

			createFakeCPUTopology(t, tmpDir, tc.topology)

			provider := NewSystemCPUInfo()
			cpuInfos, err := provider.GetCPUInfos()
			if err != nil {
				t.Fatalf("GetCPUInfos() failed: %v", err)
			}
			if len(cpuInfos) != len(tc.expectedInfos) {
				t.Fatalf("expected %d cpu infos, got %d", len(tc.expectedInfos), len(cpuInfos))
			}

			sort.Slice(cpuInfos, func(i, j int) bool {
				return cpuInfos[i].CpuID < cpuInfos[j].CpuID
			})

			if !reflect.DeepEqual(cpuInfos, tc.expectedInfos) {
				t.Errorf("Mismatch in CPUInfo.\nExpected: %+v\nGot:      %+v", tc.expectedInfos, cpuInfos)
			}
		})
	}
}

func TestGetCPUInfos_ErrorScenarios(t *testing.T) {
	baseTopo := fakeCPUTopology{
		numSockets: 1, numNumaNodesPerSocket: 1, numCoresPerNumaNode: 1, cpusPerCore: 1, coresPerL3: 1,
	}

	testCases := []struct {
		name                   string
		setup                  func(t *testing.T, dir string)
		expectedErrorSubstring string
		expectedInfos          []CPUInfo // For non-error cases to ensure graceful handling
	}{
		{
			name: "missing physical_package_id",
			setup: func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, "sys/devices/system/cpu/cpu0/topology/physical_package_id")); err != nil {
					t.Fatal(err)
				}
			},
			expectedErrorSubstring: "", // Should warn and continue, not error out
			expectedInfos: []CPUInfo{
				{CpuID: 0, CoreID: 0, SocketID: 0, NumaNode: 0, NumaNodeAffinityMask: "0x1", SiblingCpuID: -1, CoreType: CoreTypeStandard, L3CacheID: 0},
			},
		},
		{
			name: "missing cpumap",
			setup: func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, "sys/devices/system/node/node0/cpumap")); err != nil {
					t.Fatal(err)
				}
			},
			expectedErrorSubstring: "cpumap",
		},
		{
			name: "missing shared_cpu_list",
			setup: func(t *testing.T, dir string) {
				if err := os.Remove(filepath.Join(dir, "sys/devices/system/cpu/cpu0/cache/index3/shared_cpu_list")); err != nil {
					t.Fatal(err)
				}
			},
			expectedErrorSubstring: "shared_cpu_list",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			t.Setenv("HOST_ROOT", tmpDir)

			// Create the base topology for all error scenarios.
			createFakeCPUTopology(t, tmpDir, baseTopo)
			// Apply the specific modification for the current test case.
			tc.setup(t, tmpDir)

			provider := NewSystemCPUInfo()
			cpuInfos, err := provider.GetCPUInfos()
			if tc.expectedErrorSubstring != "" {
				if err == nil {
					t.Fatal("expected an error, but got none")
				}
				if !strings.Contains(err.Error(), tc.expectedErrorSubstring) {
					t.Errorf("expected error to contain %q, but got: %v", tc.expectedErrorSubstring, err)
				}
			} else {
				if err != nil {
					t.Errorf("did not expect an error, but got: %v", err)
				}
				if !reflect.DeepEqual(cpuInfos, tc.expectedInfos) {
					t.Errorf("Mismatch in CPUInfo.\nExpected: %+v\nGot:      %+v", tc.expectedInfos, cpuInfos)
				}
			}
		})
	}
}
