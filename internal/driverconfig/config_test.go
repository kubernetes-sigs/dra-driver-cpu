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

	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/driverconfig"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
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

// TestResolve_NoSources: no sources returns Default() unchanged.
func TestResolve_NoSources(t *testing.T) {
	result, err := driverconfig.Resolve(testr.New(t), nil)

	require.NoError(t, err)
	assert.Equal(t, driverconfig.Default(), result)
}

// TestResolve_FileOverridesDefaults: file values are applied when no CLI flags are set.
func TestResolve_FileOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
apiVersion: v1alpha1
cpuDeviceMode: individual
groupBy: socket
reservedCPUs: "0-3"
sysfsOverlay: /custom/sysfs
`)

	result, err := driverconfig.Resolve(testr.New(t), []driverconfig.Source{
		driverconfig.FromFile(cfgFile),
	})

	require.NoError(t, err)
	assert.Equal(t, device.CPU_DEVICE_MODE_INDIVIDUAL, result.CPUDeviceMode)
	assert.Equal(t, device.GROUP_BY_SOCKET, result.GroupBy)
	assert.Equal(t, "0-3", result.ReservedCPUs)
	assert.Equal(t, "/custom/sysfs", result.SysFSOverlay)
}

// TestResolve_CLIFlagWinsOverFile: an explicitly-passed CLI flag beats the file value.
func TestResolve_CLIFlagWinsOverFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
apiVersion: v1alpha1
reservedCPUs: "0-3"
cpuDeviceMode: individual
`)

	var cfg driverconfig.Config
	fs := newFlagSet(t, &cfg, []string{
		"--reserved-cpus=4-7",
		"--cpu-device-mode=grouped",
	})

	result, err := driverconfig.Resolve(testr.New(t), []driverconfig.Source{
		driverconfig.FromFile(cfgFile),
		driverconfig.FromFlags(fs),
	})

	require.NoError(t, err)
	assert.Equal(t, "4-7", result.ReservedCPUs)
	assert.Equal(t, device.CPU_DEVICE_MODE_GROUPED, result.CPUDeviceMode)
}

// TestResolve_PartialFile: fields absent from the file retain their default values.
func TestResolve_PartialFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
apiVersion: v1alpha1
reservedCPUs: "4-7"
`)

	result, err := driverconfig.Resolve(testr.New(t), []driverconfig.Source{
		driverconfig.FromFile(cfgFile),
	})

	require.NoError(t, err)
	assert.Equal(t, "4-7", result.ReservedCPUs)
	assert.Equal(t, ":8080", result.BindAddress)
	assert.Equal(t, device.CPU_DEVICE_MODE_GROUPED, result.CPUDeviceMode)
	assert.Equal(t, device.GROUP_BY_NUMA_NODE, result.GroupBy)
}

// TestResolve_FileWithoutAPIVersion: omitting apiVersion is accepted.
func TestResolve_FileWithoutAPIVersion(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
reservedCPUs: "5-6"
`)

	result, err := driverconfig.Resolve(testr.New(t), []driverconfig.Source{
		driverconfig.FromFile(cfgFile),
	})

	require.NoError(t, err)
	assert.Equal(t, "5-6", result.ReservedCPUs)
}

// TestResolve_UnknownAPIVersionIsError: an unrecognised apiVersion is rejected.
func TestResolve_UnknownAPIVersionIsError(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
apiVersion: v99
reservedCPUs: "0-3"
`)

	_, err := driverconfig.Resolve(testr.New(t), []driverconfig.Source{
		driverconfig.FromFile(cfgFile),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported apiVersion")
	assert.Contains(t, err.Error(), "v99")
}

// TestResolve_MissingFileIsError: a non-existent file path returns an error.
func TestResolve_MissingFileIsError(t *testing.T) {
	_, err := driverconfig.Resolve(testr.New(t), []driverconfig.Source{
		driverconfig.FromFile("/does/not/exist/config.yaml"),
	})

	require.Error(t, err)
}

// TestResolve_EmptyFilePath: an empty file path is a no-op.
func TestResolve_EmptyFilePath(t *testing.T) {
	result, err := driverconfig.Resolve(testr.New(t), []driverconfig.Source{
		driverconfig.FromFile(""),
	})

	require.NoError(t, err)
	assert.Equal(t, driverconfig.Default(), result)
}

// TestDefault pins the built-in default values.
func TestDefault(t *testing.T) {
	d := driverconfig.Default()

	assert.Equal(t, ":8080", d.BindAddress)
	assert.Equal(t, device.CPU_DEVICE_MODE_GROUPED, d.CPUDeviceMode)
	assert.Equal(t, device.GROUP_BY_NUMA_NODE, d.GroupBy)
	// Fields with no built-in default must be zero/empty.
	assert.Empty(t, d.Kubeconfig)
	assert.Empty(t, d.HostnameOverride)
	assert.Empty(t, d.ReservedCPUs)
	assert.False(t, d.ExposePCIeRoots)
}

// TestResolve_InvalidCPUDeviceModeIsError: an invalid cpuDeviceMode in the file is rejected.
func TestResolve_InvalidCPUDeviceModeIsError(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
apiVersion: v1alpha1
cpuDeviceMode: garbage
`)

	_, err := driverconfig.Resolve(testr.New(t), []driverconfig.Source{
		driverconfig.FromFile(cfgFile),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid cpuDeviceMode")
	assert.Contains(t, err.Error(), "garbage")
}

// TestResolve_InvalidGroupByIsError: an invalid groupBy in the file is rejected.
func TestResolve_InvalidGroupByIsError(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
apiVersion: v1alpha1
groupBy: garbage
`)

	_, err := driverconfig.Resolve(testr.New(t), []driverconfig.Source{
		driverconfig.FromFile(cfgFile),
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid groupBy")
	assert.Contains(t, err.Error(), "garbage")
}

// TestResolve_ExcludedFieldInFileIsError: excluded and removed fields aren't
// configurable via the config file.
func TestResolve_ExcludedFieldInFileIsError(t *testing.T) {
	for _, tc := range []struct {
		field         string
		content       string
		expectedError string
	}{
		{field: "bindAddress", content: "bindAddress: \":9090\"", expectedError: "not configurable via the config file"},
		{field: "exposePCIeRoots", content: "exposePCIeRoots: true", expectedError: "not configurable via the config file"},
		{field: "showMetrics", content: "showMetrics: true", expectedError: "unknown field"},
	} {
		t.Run(tc.field, func(t *testing.T) {
			dir := t.TempDir()
			cfgFile := writeFile(t, dir, "config.yaml", "apiVersion: v1alpha1\n"+tc.content+"\n")

			_, err := driverconfig.Resolve(testr.New(t), []driverconfig.Source{
				driverconfig.FromFile(cfgFile),
			})

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.field)
			assert.Contains(t, err.Error(), tc.expectedError)
		})
	}
}

// TestResolve_BoolFlagWinsOverFile: a bool CLI flag correctly overrides via the JSON round-trip.
func TestResolve_BoolFlagWinsOverFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
apiVersion: v1alpha1
reservedCPUs: "0-3"
`)

	var cfg driverconfig.Config
	fs := newFlagSet(t, &cfg, []string{
		"--expose-pcie-roots=true",
	})

	result, err := driverconfig.Resolve(testr.New(t), []driverconfig.Source{
		driverconfig.FromFile(cfgFile),
		driverconfig.FromFlags(fs),
	})

	require.NoError(t, err)
	assert.True(t, result.ExposePCIeRoots)
	assert.Equal(t, "0-3", result.ReservedCPUs)
}
