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

package gatherinfo_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/driverconfig"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/gatherinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/driver"
	"sigs.k8s.io/yaml"
)

func TestRunWritesReportFile(t *testing.T) {
	setupFakeHost(t, []byte("/dracpu\x00--reserved-cpus=4-7\x00--cpu-device-mode\x00individual\x00--group-by=socket\x00--hostname-override=node-a\x00--v=4\x00--logging-format=json\x00"))

	outputDir := filepath.Join(t.TempDir(), "reports")
	err := gatherinfo.Run([]string{"--output-dir=" + outputDir}, gatherinfo.Options{
		DriverConfig: driverconfig.Default(),
	}, logr.Discard())
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	entries, err := os.ReadDir(outputDir)
	if err != nil {
		t.Fatalf("failed to read output dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d output files, want 1", len(entries))
	}
	outputName := entries[0].Name()
	if entries[0].IsDir() || !strings.HasPrefix(outputName, "dracpu-gatherinfo-") || !strings.HasSuffix(outputName, ".yaml") {
		t.Fatalf("unexpected output file name %q", outputName)
	}

	data, err := os.ReadFile(filepath.Join(outputDir, outputName))
	if err != nil {
		t.Fatalf("failed to read output file: %v", err)
	}
	report := readReport(t, data)
	assertCommonReport(t, report)

	if report.DriverConfig.ReservedCPUs != "4-7" {
		t.Fatalf("reservedCPUs = %q, want 4-7", report.DriverConfig.ReservedCPUs)
	}
	if report.DriverConfig.CPUDeviceMode != driver.CPU_DEVICE_MODE_INDIVIDUAL {
		t.Fatalf("cpuDeviceMode = %q, want %q", report.DriverConfig.CPUDeviceMode, driver.CPU_DEVICE_MODE_INDIVIDUAL)
	}
	if report.DriverConfig.GroupBy != driver.GROUP_BY_SOCKET {
		t.Fatalf("groupBy = %q, want %q", report.DriverConfig.GroupBy, driver.GROUP_BY_SOCKET)
	}
	if report.DriverConfig.HostnameOverride != "node-a" {
		t.Fatalf("hostnameOverride = %q, want node-a", report.DriverConfig.HostnameOverride)
	}
}

func TestRunWritesReportToStdout(t *testing.T) {
	setupFakeHost(t, []byte("/dracpu\x00--group-by=socket\x00"))

	stdout, err := captureStdout(t, func() error {
		return gatherinfo.Run([]string{"--stdout"}, gatherinfo.Options{
			DriverConfig: driverconfig.Default(),
		}, logr.Discard())
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	report := readReport(t, []byte(stdout))
	assertCommonReport(t, report)
	if report.DriverConfig.GroupBy != driver.GROUP_BY_SOCKET {
		t.Fatalf("groupBy = %q, want %q", report.DriverConfig.GroupBy, driver.GROUP_BY_SOCKET)
	}
}

func TestRunFailsWhenDriverProcessIsMissing(t *testing.T) {
	setupFakeHost(t, nil)

	_, err := captureStdout(t, func() error {
		return gatherinfo.Run([]string{"--stdout"}, gatherinfo.Options{
			DriverConfig: driverconfig.Default(),
		}, logr.Discard())
	})
	if err == nil {
		t.Fatal("Run succeeded, want error")
	}
	if !strings.Contains(err.Error(), `failed to find running "dracpu" process`) {
		t.Fatalf("error = %v, want missing dracpu process error", err)
	}
}

// TestRunReadsConfigFile: config-file values appear in the report when --config is used.
func TestRunReadsConfigFile(t *testing.T) {
	cfgFile := filepath.Join(t.TempDir(), "driver-config.yaml")
	if err := os.WriteFile(cfgFile, []byte("reservedCPUs: \"2-3\"\n"), 0600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	setupFakeHost(t, []byte("/dracpu\x00--config="+cfgFile+"\x00"))

	stdout, err := captureStdout(t, func() error {
		return gatherinfo.Run([]string{"--stdout"}, gatherinfo.Options{
			DriverConfig: driverconfig.Default(),
		}, logr.Discard())
	})
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	report := readReport(t, []byte(stdout))
	if report.DriverConfig.ReservedCPUs != "2-3" {
		t.Fatalf("reservedCPUs = %q, want 2-3 (from config file)", report.DriverConfig.ReservedCPUs)
	}
}

func TestRunRejectsStdoutWithOutputDir(t *testing.T) {
	err := gatherinfo.Run([]string{"--stdout", "--output-dir=/tmp"}, gatherinfo.Options{}, logr.Discard())
	if err == nil {
		t.Fatal("Run succeeded, want error")
	}
}

func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()

	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create stdout pipe: %v", err)
	}

	os.Stdout = writer
	runErr := fn()
	os.Stdout = original

	closeErr := writer.Close()
	data, readErr := io.ReadAll(reader)
	readerCloseErr := reader.Close()

	return string(data), errors.Join(runErr, closeErr, readErr, readerCloseErr)
}

func readReport(t *testing.T, data []byte) gatherinfo.Report {
	t.Helper()

	if !strings.HasPrefix(string(data), "---\n") {
		t.Fatalf("yaml output does not start with document marker: %q", string(data))
	}

	var report gatherinfo.Report
	if err := yaml.Unmarshal(data, &report); err != nil {
		t.Fatalf("failed to unmarshal report YAML: %v", err)
	}
	return report
}

func assertCommonReport(t *testing.T, report gatherinfo.Report) {
	t.Helper()

	if report.LayoutVersion != gatherinfo.LayoutVersion {
		t.Fatalf("layoutVersion = %q, want %q", report.LayoutVersion, gatherinfo.LayoutVersion)
	}
	if report.CPUDetails.Topology.NumCPUs != 1 {
		t.Fatalf("numCPUs = %d, want 1", report.CPUDetails.Topology.NumCPUs)
	}
	if report.CPUDetails.Topology.NumCores != 1 {
		t.Fatalf("numCores = %d, want 1", report.CPUDetails.Topology.NumCores)
	}
	if report.CPUDetails.Topology.NumUncoreCache != 1 {
		t.Fatalf("numUncoreCache = %d, want 1", report.CPUDetails.Topology.NumUncoreCache)
	}
	if report.CPUDetails.Topology.NumSockets != 1 {
		t.Fatalf("numSockets = %d, want 1", report.CPUDetails.Topology.NumSockets)
	}
	if report.CPUDetails.Topology.NumNUMANodes != 1 {
		t.Fatalf("numNUMANodes = %d, want 1", report.CPUDetails.Topology.NumNUMANodes)
	}
	if report.CPUDetails.Topology.SMTEnabled {
		t.Fatal("smtEnabled = true, want false")
	}
	if len(report.CPUDetails.CPUs) != 1 {
		t.Fatalf("got %d CPUs, want 1", len(report.CPUDetails.CPUs))
	}

	cpu := report.CPUDetails.CPUs[0]
	if cpu.CPUID != 0 {
		t.Fatalf("cpuID = %d, want 0", cpu.CPUID)
	}
	if cpu.CoreID != 0 {
		t.Fatalf("coreID = %d, want 0", cpu.CoreID)
	}
	if cpu.SocketID != 0 {
		t.Fatalf("socketID = %d, want 0", cpu.SocketID)
	}
	if cpu.ClusterID != 0 {
		t.Fatalf("clusterID = %d, want 0", cpu.ClusterID)
	}
	if cpu.NUMANodeID != 0 {
		t.Fatalf("numaNodeID = %d, want 0", cpu.NUMANodeID)
	}
	if cpu.NUMANodeCPUSet != "0" {
		t.Fatalf("numaNodeCPUSet = %q, want 0", cpu.NUMANodeCPUSet)
	}
	if cpu.Sibling != -1 {
		t.Fatalf("sibling = %d, want -1", cpu.Sibling)
	}
	if cpu.CoreType != "standard" {
		t.Fatalf("coreType = %q, want standard", cpu.CoreType)
	}
	if cpu.UncoreCacheID != 0 {
		t.Fatalf("uncoreCacheID = %d, want 0", cpu.UncoreCacheID)
	}
}

func setupFakeHost(t *testing.T, driverCmdline []byte) {
	t.Helper()

	hostRoot := t.TempDir()
	t.Setenv("HOST_ROOT", hostRoot)

	writeTestFile(t, filepath.Join(hostRoot, "proc", "1", "comm"), []byte("pause\n"))
	writeTestFile(t, filepath.Join(hostRoot, "proc", "1", "cmdline"), []byte("/pause\x00"))
	if driverCmdline != nil {
		writeTestFile(t, filepath.Join(hostRoot, "proc", "42", "comm"), []byte("dracpu\n"))
		writeTestFile(t, filepath.Join(hostRoot, "proc", "42", "cmdline"), driverCmdline)
	}

	writeTestFile(t, filepath.Join(hostRoot, "sys", "devices", "system", "cpu", "online"), []byte("0\n"))
	writeTestFile(t, filepath.Join(hostRoot, "sys", "devices", "system", "cpu", "smt", "control"), []byte("off\n"))
	writeTestFile(t, filepath.Join(hostRoot, "sys", "devices", "system", "node", "node0", "cpulist"), []byte("0\n"))
	writeTestFile(t, filepath.Join(hostRoot, "sys", "devices", "system", "cpu", "cpu0", "topology", "physical_package_id"), []byte("0\n"))
	writeTestFile(t, filepath.Join(hostRoot, "sys", "devices", "system", "cpu", "cpu0", "topology", "cluster_id"), []byte("0\n"))
	writeTestFile(t, filepath.Join(hostRoot, "sys", "devices", "system", "cpu", "cpu0", "topology", "core_id"), []byte("0\n"))
	writeTestFile(t, filepath.Join(hostRoot, "sys", "devices", "system", "cpu", "cpu0", "cache", "index3", "level"), []byte("3\n"))
	writeTestFile(t, filepath.Join(hostRoot, "sys", "devices", "system", "cpu", "cpu0", "cache", "index3", "shared_cpu_list"), []byte("0\n"))
	writeTestFile(t, filepath.Join(hostRoot, "sys", "devices", "system", "cpu", "cpu0", "cache", "index3", "id"), []byte("0\n"))

	if err := os.MkdirAll(filepath.Join(hostRoot, "sys", "devices", "system", "cpu", "cpu0", "node0"), 0755); err != nil {
		t.Fatalf("failed to create CPU NUMA node dir: %v", err)
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
