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

package gatherinfo

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/driverconfig"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/driver"
	"k8s.io/utils/cpuset"
	"sigs.k8s.io/yaml"
)

func TestReadCmdlineFile(t *testing.T) {
	tests := []struct {
		name    string
		content []byte
		want    []string
	}{
		{
			name:    "nul separated args",
			content: []byte("/dracpu\x00--cpu-device-mode=individual\x00--group-by\x00socket\x00"),
			want:    []string{"/dracpu", "--cpu-device-mode=individual", "--group-by", "socket"},
		},
		{
			name:    "without trailing nul",
			content: []byte("/dracpu\x00--v=4"),
			want:    []string{"/dracpu", "--v=4"},
		},
		{
			name: "empty file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cmdline")
			if err := os.WriteFile(path, tt.content, 0600); err != nil {
				t.Fatalf("failed to write cmdline file: %v", err)
			}

			args, err := readCmdlineFile(path)
			if err != nil {
				t.Fatalf("readCmdlineFile failed: %v", err)
			}
			if !slices.Equal(args, tt.want) {
				t.Fatalf("args = %v, want %v", args, tt.want)
			}
		})
	}
}

func TestDetectDriverConfig(t *testing.T) {
	defaults := driverconfig.Default()
	if defaults.CPUDeviceMode != driver.CPU_DEVICE_MODE_GROUPED {
		t.Fatalf("CPUDeviceMode = %q, want %q", defaults.CPUDeviceMode, driver.CPU_DEVICE_MODE_GROUPED)
	}
	if defaults.GroupBy != driver.GROUP_BY_NUMA_NODE {
		t.Fatalf("GroupBy = %q, want %q", defaults.GroupBy, driver.GROUP_BY_NUMA_NODE)
	}

	tests := []struct {
		name    string
		content []byte
		want    driverconfig.Config
	}{
		{
			name: "fallback when cmdline is missing",
			want: defaults,
		},
		{
			name:    "from driver cmdline",
			content: []byte("/dracpu\x00--reserved-cpus=4-7\x00--cpu-device-mode\x00individual\x00--group-by=socket\x00--hostname-override\x00node-a\x00"),
			want: driverconfig.Config{
				BindAddress:      defaults.BindAddress,
				ReservedCPUs:     "4-7",
				CPUDeviceMode:    driver.CPU_DEVICE_MODE_INDIVIDUAL,
				GroupBy:          driver.GROUP_BY_SOCKET,
				HostnameOverride: "node-a",
			},
		},
		{
			name:    "ignores unknown driver flags",
			content: []byte("/dracpu\x00--v=4\x00--reserved-cpus=8-11\x00--logging-format=json\x00"),
			want: driverconfig.Config{
				BindAddress:   defaults.BindAddress,
				ReservedCPUs:  "8-11",
				CPUDeviceMode: defaults.CPUDeviceMode,
				GroupBy:       defaults.GroupBy,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "cmdline")
			if tt.content != nil {
				if err := os.WriteFile(path, tt.content, 0600); err != nil {
					t.Fatalf("failed to write cmdline file: %v", err)
				}
			}

			cfg := detectDriverConfig(defaults, path)
			if cfg != tt.want {
				t.Fatalf("cfg = %+v, want %+v", cfg, tt.want)
			}
		})
	}
}

func TestDefaultDriverCmdlinePathFindsDriverProcess(t *testing.T) {
	hostRoot := t.TempDir()
	t.Setenv("HOST_ROOT", hostRoot)

	writeTestFile(t, filepath.Join(hostRoot, "proc", "1", "comm"), []byte("pause\n"))
	writeTestFile(t, filepath.Join(hostRoot, "proc", "1", "cmdline"), []byte("/pause\x00"))
	writeTestFile(t, filepath.Join(hostRoot, "proc", "42", "comm"), []byte("dracpu\n"))
	writeTestFile(t, filepath.Join(hostRoot, "proc", "42", "cmdline"), []byte("/dracpu\x00--group-by=socket\x00"))

	path, err := defaultDriverCmdlinePath()
	if err != nil {
		t.Fatalf("defaultDriverCmdlinePath failed: %v", err)
	}
	if !strings.HasSuffix(path, filepath.Join("proc", "42", "cmdline")) {
		t.Fatalf("driverCmdlinePath = %q, want proc/42/cmdline suffix", path)
	}
}

func TestDefaultDriverCmdlinePathFailsWhenDriverProcessIsMissing(t *testing.T) {
	hostRoot := t.TempDir()
	t.Setenv("HOST_ROOT", hostRoot)

	writeTestFile(t, filepath.Join(hostRoot, "proc", "1", "comm"), []byte("pause\n"))
	writeTestFile(t, filepath.Join(hostRoot, "proc", "1", "cmdline"), []byte("/pause\x00"))

	_, err := defaultDriverCmdlinePath()
	if err == nil {
		t.Fatal("defaultDriverCmdlinePath succeeded, want error")
	}
}

func TestCreateOutputFileTempFile(t *testing.T) {
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)

	f, outputPath, err := createOutputFile("", now, 42)
	if err != nil {
		t.Fatalf("createOutputFile failed: %v", err)
	}
	t.Cleanup(func() {
		f.Close()
		os.Remove(outputPath)
	})

	info, err := os.Stat(outputPath)
	if err != nil {
		t.Fatalf("output file missing: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("temp file size = %d, want 0", info.Size())
	}
	if filepath.Ext(outputPath) != ".yaml" {
		t.Fatalf("output path = %q, want .yaml suffix", outputPath)
	}
}

func TestCreateOutputFileParentDir(t *testing.T) {
	parentDir := t.TempDir()
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)

	f, outputPath, err := createOutputFile(parentDir, now, 42)
	if err != nil {
		t.Fatalf("createOutputFile failed: %v", err)
	}
	t.Cleanup(func() {
		f.Close()
	})

	if filepath.Dir(outputPath) != parentDir {
		t.Fatalf("output parent = %q, want %q", filepath.Dir(outputPath), parentDir)
	}
	if filepath.Base(outputPath) != "dracpu-gatherinfo-20260529-120000-42.yaml" {
		t.Fatalf("output name = %q", filepath.Base(outputPath))
	}
}

func TestCreateOutputFileCleansParentDir(t *testing.T) {
	parentDir := filepath.Join(t.TempDir(), "nested", "..", "out")
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)

	f, outputPath, err := createOutputFile(parentDir, now, 42)
	if err != nil {
		t.Fatalf("createOutputFile failed: %v", err)
	}
	t.Cleanup(func() {
		f.Close()
	})

	if filepath.Dir(outputPath) != filepath.Clean(parentDir) {
		t.Fatalf("output parent = %q, want %q", filepath.Dir(outputPath), filepath.Clean(parentDir))
	}
}

func TestCreateOutputFileFailsWhenFileExists(t *testing.T) {
	parentDir := t.TempDir()
	now := time.Date(2026, 5, 29, 12, 0, 0, 0, time.UTC)
	outputPath := filepath.Join(parentDir, "dracpu-gatherinfo-20260529-120000-42.yaml")
	if err := os.WriteFile(outputPath, []byte("existing"), 0600); err != nil {
		t.Fatalf("failed to seed output file: %v", err)
	}

	_, _, err := createOutputFile(parentDir, now, 42)
	if err == nil {
		t.Fatal("createOutputFile succeeded, want error")
	}
}

func TestWriteReportFile(t *testing.T) {
	tmpDir := t.TempDir()
	testFile := filepath.Join(tmpDir, "test.yaml")

	testData := map[string]any{
		"key1": "value1",
		"key2": 42,
		"key3": []string{"a", "b", "c"},
	}

	f, err := os.OpenFile(testFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatalf("failed to create output file: %v", err)
	}
	if err := writeReportFile(f, testFile, testData); err != nil {
		t.Fatalf("writeReportFile failed: %v", err)
	}

	data, err := os.ReadFile(testFile)
	if err != nil {
		t.Fatalf("failed to read YAML file: %v", err)
	}
	if !strings.HasPrefix(string(data), "---\n") {
		t.Fatalf("yaml output does not start with document marker: %q", string(data))
	}

	var result map[string]any
	if err := yaml.Unmarshal(data, &result); err != nil {
		t.Fatalf("failed to unmarshal YAML: %v", err)
	}

	if result["key1"] != "value1" {
		t.Errorf("expected key1=value1, got %v", result["key1"])
	}
	if result["key2"].(float64) != 42 {
		t.Errorf("expected key2=42, got %v", result["key2"])
	}
}

func TestWriteYAML(t *testing.T) {
	var buf bytes.Buffer
	testData := map[string]any{
		"layoutVersion": LayoutVersion,
	}

	if err := writeYAML(&buf, testData); err != nil {
		t.Fatalf("writeYAML failed: %v", err)
	}

	output := buf.String()
	if !strings.HasPrefix(output, "---\n") {
		t.Fatalf("stdout yaml does not start with document marker: %q", output)
	}
	if !strings.Contains(output, "layoutVersion: v1\n") {
		t.Fatalf("stdout yaml = %q, want layoutVersion", output)
	}
}

func TestMarshalYAMLReportShape(t *testing.T) {
	report := Report{
		ToolVersion:   ToolVersion{GoVersion: "go1.26.0"},
		LayoutVersion: LayoutVersion,
		CPUDetails: CPUDetails{
			Topology: TopologySummary{
				NumCPUs:        16,
				NumCores:       8,
				NumUncoreCache: 1,
				NumSockets:     1,
				NumNUMANodes:   1,
				SMTEnabled:     true,
			},
			CPUs: []CPU{
				{
					CPUID:          0,
					CoreID:         0,
					SocketID:       0,
					ClusterID:      -1,
					NUMANodeID:     0,
					NUMANodeCPUSet: "0-15",
					Sibling:        8,
					CoreType:       "standard",
					UncoreCacheID:  0,
				},
			},
		},
		DriverConfig: driverconfig.Config{
			CPUDeviceMode: "grouped",
			GroupBy:       "numanode",
			BindAddress:   ":8080",
		},
	}

	data, err := marshalYAML(report)
	if err != nil {
		t.Fatalf("marshalYAML failed: %v", err)
	}

	output := string(data)
	for _, want := range []string{
		"layoutVersion: v1\n",
		"numCPUs: 16\n",
		"smtEnabled: true\n",
		"numaNodeCPUSet: 0-15\n",
		"coreType: standard\n",
		"cpuDeviceMode: grouped\n",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("yaml output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "CPUDetails:") {
		t.Fatalf("yaml output leaked internal topology details:\n%s", output)
	}
}

func TestMakeCPUList(t *testing.T) {
	cpus := []cpuinfo.CPUInfo{
		{
			CpuID:          0,
			CoreID:         0,
			SocketID:       0,
			ClusterID:      -1,
			NUMANodeID:     0,
			NumaNodeCPUSet: cpuset.New(0, 1, 2, 3),
			SiblingCPUID:   1,
			CoreType:       cpuinfo.CoreTypeStandard,
			UncoreCacheID:  0,
		},
	}

	got := makeCPUList(cpus)
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1", len(got))
	}
	if got[0].NUMANodeCPUSet != "0-3" {
		t.Fatalf("NUMANodeCPUSet = %q, want 0-3", got[0].NUMANodeCPUSet)
	}
	if got[0].CoreType != "standard" {
		t.Fatalf("CoreType = %q, want standard", got[0].CoreType)
	}
}

func TestMarshalYAMLErrors(t *testing.T) {
	_, err := marshalYAML(make(chan int))
	if err == nil {
		t.Fatal("marshalYAML succeeded, want error")
	}
}

func TestWriteReportFileErrors(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(t *testing.T) (file *os.File, path string, data any)
		wantErr bool
	}{
		{
			name: "marshal error with invalid data",
			setup: func(t *testing.T) (*os.File, string, any) {
				path := filepath.Join(t.TempDir(), "test.yaml")
				file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
				if err != nil {
					t.Fatalf("failed to create output file: %v", err)
				}
				return file, path, make(chan int)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file, path, data := tt.setup(t)
			err := writeReportFile(file, path, data)
			if (err != nil) != tt.wantErr {
				t.Errorf("writeReportFile() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestWriteReportFileCleansUpOnFailure(t *testing.T) {
	outputPath := filepath.Join(t.TempDir(), "report.yaml")
	file, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		t.Fatalf("failed to create output file: %v", err)
	}

	err = writeReportFile(file, outputPath, make(chan int))
	if err == nil {
		t.Fatal("writeReportFile succeeded, want error")
	}
	if _, statErr := os.Stat(outputPath); !os.IsNotExist(statErr) {
		t.Fatalf("output file still exists after failure: %v", statErr)
	}
}

func TestRunRejectsStdoutWithOutputDir(t *testing.T) {
	err := Run([]string{"--stdout", "--output-dir=/tmp"}, Options{}, logr.Discard())
	if err == nil {
		t.Fatal("Run succeeded, want error")
	}
}

func writeTestFile(t *testing.T, path string, data []byte) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("failed to create parent dir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("failed to write %s: %v", path, err)
	}
}
