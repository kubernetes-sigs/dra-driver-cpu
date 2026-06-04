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

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGatherinfoEntrypointAcceptsGatherinfoFlags(t *testing.T) {
	tmpDir := t.TempDir()
	binPath := filepath.Join(tmpDir, "dracpu-gatherinfo")
	outputDir := filepath.Join(tmpDir, "out")
	hostRoot := filepath.Join(tmpDir, "host")

	createMinimalHostRoot(t, hostRoot)

	build := exec.Command("go", "build", "-o", binPath, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("failed to build dracpu-gatherinfo test binary: %v\n%s", err, output)
	}

	cmd := exec.Command(binPath, "--output-dir="+outputDir)
	cmd.Env = append(os.Environ(), "HOST_ROOT="+hostRoot)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dracpu-gatherinfo failed: %v\n%s", err, output)
	}

	files, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatalf("failed to read output dir: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("output files = %d, want 1", len(files))
	}
}

func createMinimalHostRoot(t *testing.T, root string) {
	t.Helper()

	writeFile(t, filepath.Join(root, "proc/1/cmdline"), []byte("/dracpu\x00--cpu-device-mode=individual\x00--group-by=socket\x00"))
	writeFile(t, filepath.Join(root, "sys/devices/system/cpu/online"), []byte("0\n"))
	writeFile(t, filepath.Join(root, "sys/devices/system/cpu/smt/control"), []byte("off\n"))
	writeFile(t, filepath.Join(root, "sys/devices/system/node/node0/cpulist"), []byte("0\n"))

	cpuDir := filepath.Join(root, "sys/devices/system/cpu/cpu0")
	if err := os.MkdirAll(filepath.Join(cpuDir, "node0"), 0755); err != nil {
		t.Fatalf("failed to create NUMA marker: %v", err)
	}
	writeFile(t, filepath.Join(cpuDir, "topology/physical_package_id"), []byte("0\n"))
	writeFile(t, filepath.Join(cpuDir, "topology/cluster_id"), []byte("0\n"))
	writeFile(t, filepath.Join(cpuDir, "topology/core_id"), []byte("0\n"))
	writeFile(t, filepath.Join(cpuDir, "cache/index3/level"), []byte("3\n"))
	writeFile(t, filepath.Join(cpuDir, "cache/index3/shared_cpu_list"), []byte("0\n"))
	writeFile(t, filepath.Join(cpuDir, "cache/index3/id"), []byte("0\n"))
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("failed to create parent dir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
}
