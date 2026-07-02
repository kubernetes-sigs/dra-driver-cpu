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

// The test file lives in package driverconfig_test (external test package) to
// verify the exported API without access to internal helpers.
package driverconfig_test

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/driverconfig"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/driver"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newFlagSet creates a FlagSet with cfg registered and args parsed.
func newFlagSet(t *testing.T, cfg *driverconfig.Config, args []string) *flag.FlagSet {
	t.Helper()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg.AddFlags(fs)
	require.NoError(t, fs.Parse(args))
	return fs
}

// writeFile creates name with content inside dir and returns the full path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0600))
	return path
}

// TestLoad_NoFile: empty filePath returns base unchanged.
func TestLoad_NoFile(t *testing.T) {
	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, nil)

	result, err := driverconfig.Load(cfg, "", fs, logr.Discard())

	require.NoError(t, err)
	assert.Equal(t, cfg, result)
}

// TestLoad_FileOverridesDefaults: file values are applied when no CLI flags are set.
func TestLoad_FileOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
apiVersion: v1alpha1
bindAddress: ":9090"
cpuDeviceMode: individual
groupBy: socket
reservedCPUs: "0-3"
exposePCIeRoots: true
`)

	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, nil) // no CLI flags

	result, err := driverconfig.Load(cfg, cfgFile, fs, logr.Discard())

	require.NoError(t, err)
	assert.Equal(t, ":9090", result.BindAddress)
	assert.Equal(t, driver.CPU_DEVICE_MODE_INDIVIDUAL, result.CPUDeviceMode)
	assert.Equal(t, driver.GROUP_BY_SOCKET, result.GroupBy)
	assert.Equal(t, "0-3", result.ReservedCPUs)
	assert.True(t, result.ExposePCIeRoots)
}

// TestLoad_CLIFlagWinsOverFile: an explicitly-passed CLI flag beats the file value.
func TestLoad_CLIFlagWinsOverFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
apiVersion: v1alpha1
bindAddress: ":9090"
cpuDeviceMode: individual
`)

	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, []string{
		"--bind-address=:7070",
		"--cpu-device-mode=grouped",
	})

	result, err := driverconfig.Load(cfg, cfgFile, fs, logr.Discard())

	require.NoError(t, err)
	assert.Equal(t, ":7070", result.BindAddress)
	assert.Equal(t, driver.CPU_DEVICE_MODE_GROUPED, result.CPUDeviceMode)
}

// TestLoad_PartialFile: fields absent from the file retain their default values.
func TestLoad_PartialFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
apiVersion: v1alpha1
reservedCPUs: "4-7"
`)

	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, nil)

	result, err := driverconfig.Load(cfg, cfgFile, fs, logr.Discard())

	require.NoError(t, err)
	assert.Equal(t, "4-7", result.ReservedCPUs)
	assert.Equal(t, ":8080", result.BindAddress)
	assert.Equal(t, driver.CPU_DEVICE_MODE_GROUPED, result.CPUDeviceMode)
	assert.Equal(t, driver.GROUP_BY_NUMA_NODE, result.GroupBy)
}

// TestLoad_FileWithoutAPIVersion: omitting apiVersion is accepted.
func TestLoad_FileWithoutAPIVersion(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
bindAddress: ":9191"
`)

	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, nil)

	result, err := driverconfig.Load(cfg, cfgFile, fs, logr.Discard())

	require.NoError(t, err)
	assert.Equal(t, ":9191", result.BindAddress)
}

// TestLoad_UnknownAPIVersionIsError: an unrecognised apiVersion is rejected.
func TestLoad_UnknownAPIVersionIsError(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
apiVersion: v99
bindAddress: ":9090"
`)

	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, nil)

	_, err := driverconfig.Load(cfg, cfgFile, fs, logr.Discard())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported apiVersion")
	assert.Contains(t, err.Error(), "v99")
}

// TestLoad_MissingFileIsError: a non-existent file path returns an error.
func TestLoad_MissingFileIsError(t *testing.T) {
	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, nil)

	_, err := driverconfig.Load(cfg, "/does/not/exist/config.yaml", fs, logr.Discard())

	require.Error(t, err)
}

// TestDefault pins the built-in default values.
func TestDefault(t *testing.T) {
	d := driverconfig.Default()

	assert.Equal(t, ":8080", d.BindAddress)
	assert.Equal(t, driver.CPU_DEVICE_MODE_GROUPED, d.CPUDeviceMode)
	assert.Equal(t, driver.GROUP_BY_NUMA_NODE, d.GroupBy)
	// Fields with no built-in default must be zero/empty.
	assert.Empty(t, d.Kubeconfig)
	assert.Empty(t, d.HostnameOverride)
	assert.Empty(t, d.ReservedCPUs)
	assert.False(t, d.ExposePCIeRoots)
}

// TestLoad_InvalidCPUDeviceModeIsError: an invalid cpuDeviceMode in the file is rejected.
func TestLoad_InvalidCPUDeviceModeIsError(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
apiVersion: v1alpha1
cpuDeviceMode: garbage
`)

	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, nil)

	_, err := driverconfig.Load(cfg, cfgFile, fs, logr.Discard())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid cpuDeviceMode")
	assert.Contains(t, err.Error(), "garbage")
}

// TestLoad_InvalidGroupByIsError: an invalid groupBy in the file is rejected.
func TestLoad_InvalidGroupByIsError(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
apiVersion: v1alpha1
groupBy: garbage
`)

	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, nil)

	_, err := driverconfig.Load(cfg, cfgFile, fs, logr.Discard())

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid groupBy")
	assert.Contains(t, err.Error(), "garbage")
}
