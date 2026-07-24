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
	"strings"
	"testing"

	"github.com/go-logr/logr/funcr"
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

// TestLoad_NoFile: empty filePath returns base unchanged.
func TestLoad_NoFile(t *testing.T) {
	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, nil)

	result, err := driverconfig.Load(cfg, "", fs, testr.New(t))

	require.NoError(t, err)
	assert.Equal(t, cfg, result)
}

// TestLoad_FileOverridesDefaults: file values are applied when no CLI flags are set.
func TestLoad_FileOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
apiVersion: v1alpha1
cpuDeviceMode: individual
groupBy: socket
reservedCPUs: "0-3"
sysfsOverlay: /custom/sysfs
`)

	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, nil) // no CLI flags

	result, err := driverconfig.Load(cfg, cfgFile, fs, testr.New(t))

	require.NoError(t, err)
	assert.Equal(t, device.CPU_DEVICE_MODE_INDIVIDUAL, result.CPUDeviceMode)
	assert.Equal(t, device.GROUP_BY_SOCKET, result.GroupBy)
	assert.Equal(t, "0-3", result.ReservedCPUs)
	assert.Equal(t, "/custom/sysfs", result.SysFSOverlay)
}

// TestLoad_CLIFlagWinsOverFile: an explicitly-passed CLI flag beats the file value.
func TestLoad_CLIFlagWinsOverFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
apiVersion: v1alpha1
reservedCPUs: "0-3"
cpuDeviceMode: individual
`)

	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, []string{
		"--reserved-cpus=4-7",
		"--cpu-device-mode=grouped",
	})

	result, err := driverconfig.Load(cfg, cfgFile, fs, testr.New(t))

	require.NoError(t, err)
	assert.Equal(t, "4-7", result.ReservedCPUs)
	assert.Equal(t, device.CPU_DEVICE_MODE_GROUPED, result.CPUDeviceMode)
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

	result, err := driverconfig.Load(cfg, cfgFile, fs, testr.New(t))

	require.NoError(t, err)
	assert.Equal(t, "4-7", result.ReservedCPUs)
	assert.Equal(t, ":8080", result.BindAddress)
	assert.Equal(t, device.CPU_DEVICE_MODE_GROUPED, result.CPUDeviceMode)
	assert.Equal(t, device.GROUP_BY_NUMA_NODE, result.GroupBy)
}

// TestLoad_FileWithoutAPIVersion: omitting apiVersion is accepted.
func TestLoad_FileWithoutAPIVersion(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
reservedCPUs: "5-6"
`)

	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, nil)

	result, err := driverconfig.Load(cfg, cfgFile, fs, testr.New(t))

	require.NoError(t, err)
	assert.Equal(t, "5-6", result.ReservedCPUs)
}

// TestLoad_UnknownAPIVersionIsError: an unrecognised apiVersion is rejected.
func TestLoad_UnknownAPIVersionIsError(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
apiVersion: v99
reservedCPUs: "0-3"
`)

	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, nil)

	_, err := driverconfig.Load(cfg, cfgFile, fs, testr.New(t))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported apiVersion")
	assert.Contains(t, err.Error(), "v99")
}

// TestLoad_MissingFileIsError: a non-existent file path returns an error.
func TestLoad_MissingFileIsError(t *testing.T) {
	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, nil)

	_, err := driverconfig.Load(cfg, "/does/not/exist/config.yaml", fs, testr.New(t))

	require.Error(t, err)
}

// TestLoad_IgnoresUnrelatedFlags: flags that are not Config fields (such as
// --config and the klog --v flag that share the process FlagSet) must not
// produce any error log, while a mapped flag set on the command line still
// overrides the file. Regression guard for the spurious "flag not found in
// flagToJSONKey" errors that used to fire on every startup with a config file.
func TestLoad_IgnoresUnrelatedFlags(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
apiVersion: v1alpha1
reservedCPUs: "0-3"
hostnameOverride: from-file
`)

	cfg := driverconfig.Default()
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	// Mirror the real command line: --config and klog's --v live on the same
	// FlagSet but are not Config fields.
	var configFile string
	fs.StringVar(&configFile, "config", "", "path to the config file")
	fs.Int("v", 0, "log verbosity")
	cfg.AddFlags(fs)
	require.NoError(t, fs.Parse([]string{
		"--config=" + cfgFile,
		"--v=4",
		"--reserved-cpus=1-2",
	}))

	var logs strings.Builder
	logger := funcr.New(func(prefix, args string) {
		logs.WriteString(prefix + " " + args + "\n")
	}, funcr.Options{Verbosity: 10})

	got, err := driverconfig.Load(cfg, configFile, fs, logger)

	require.NoError(t, err)
	assert.NotContains(t, logs.String(), "flag not found",
		"unrelated flags must not produce an error log")
	// The mapped flag set on the command line still wins over the file value.
	assert.Equal(t, "1-2", got.ReservedCPUs)
	// A file-only field is still applied, so the assertions above cannot pass
	// without the file actually being read.
	assert.Equal(t, "from-file", got.HostnameOverride)
}

// TestDefault pins the built-in default values.
func TestDefault(t *testing.T) {
	d := driverconfig.Default()

	assert.Equal(t, ":8080", d.BindAddress)
	assert.Equal(t, device.CPU_DEVICE_MODE_GROUPED, d.CPUDeviceMode)
	assert.Equal(t, device.GROUP_BY_NUMA_NODE, d.GroupBy)
	// The kubelet root defaults to the standard location, so behavior is
	// unchanged unless the kubelet --root-dir is relocated.
	assert.Equal(t, "/var/lib/kubelet", d.KubeletRootDir)
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

	_, err := driverconfig.Load(cfg, cfgFile, fs, testr.New(t))

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

	_, err := driverconfig.Load(cfg, cfgFile, fs, testr.New(t))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid groupBy")
	assert.Contains(t, err.Error(), "garbage")
}

// TestLoad_ExcludedFieldInFileIsError: excluded and removed fields aren't
// configurable via the config file.
func TestLoad_ExcludedFieldInFileIsError(t *testing.T) {
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

			cfg := driverconfig.Default()
			fs := newFlagSet(t, &cfg, nil)

			_, err := driverconfig.Load(cfg, cfgFile, fs, testr.New(t))

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.field)
			assert.Contains(t, err.Error(), tc.expectedError)
		})
	}
}

// TestLoad_KubeletRootDirFromFlag: the kubelet root is supplied by the Helm
// chart as a flag, alongside the hostPath mounts derived from the same value.
func TestLoad_KubeletRootDirFromFlag(t *testing.T) {
	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, []string{"--kubelet-root-dir=/mnt/fast/k8s/kubelet"})

	result, err := driverconfig.Load(cfg, "", fs, testr.New(t))

	require.NoError(t, err)
	assert.Equal(t, "/mnt/fast/k8s/kubelet", result.KubeletRootDir)
}

// TestLoad_KubeletRootDirRejectedInFile: the kubelet root has to match the
// hostPath mounts the chart renders, so setting it in the config file (where
// the chart cannot keep the two in step) is rejected with a pointer to the
// chart value, the same way bindAddress and exposePCIeRoots are.
func TestLoad_KubeletRootDirRejectedInFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
apiVersion: v1alpha1
kubeletRootDir: /mnt/fast/k8s/kubelet
`)

	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, nil)

	_, err := driverconfig.Load(cfg, cfgFile, fs, testr.New(t))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "not configurable via the config file")
	assert.Contains(t, err.Error(), "use helm chart's kubeletRootDir instead")
}

// TestLoad_RelativeKubeletRootDirIsError: a relative kubelet root is rejected,
// since it would resolve against the working directory and break registration.
func TestLoad_RelativeKubeletRootDirIsError(t *testing.T) {
	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, []string{"--kubelet-root-dir=relative/kubelet"})

	_, err := driverconfig.Load(cfg, "", fs, testr.New(t))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be an absolute path")
}

// TestLoad_EmptyKubeletRootDirIsDefaulted: an empty kubeletRootDir is
// normalized to the default in the config layer, so the logged and gathered
// config reports the value the driver actually uses.
func TestLoad_EmptyKubeletRootDirIsDefaulted(t *testing.T) {
	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, []string{"--kubelet-root-dir="})

	result, err := driverconfig.Load(cfg, "", fs, testr.New(t))

	require.NoError(t, err)
	assert.Equal(t, "/var/lib/kubelet", result.KubeletRootDir)
}

// TestLoad_KubeletRootDirIsCleaned: a non-canonical absolute root is cleaned in
// the config layer, so the logged effective value matches the paths the driver
// and chart derive from it.
func TestLoad_KubeletRootDirIsCleaned(t *testing.T) {
	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, []string{"--kubelet-root-dir=/mnt/a/../kubelet//"})

	result, err := driverconfig.Load(cfg, "", fs, testr.New(t))

	require.NoError(t, err)
	assert.Equal(t, "/mnt/kubelet", result.KubeletRootDir)
}
