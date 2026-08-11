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
	"errors"
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
	"k8s.io/apimachinery/pkg/util/sets"
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

// TestResolve_RejectsAmbiguousKeys covers the ways a config file can reach a field
// without naming it the way the schema does. Each has to fail, and the message
// has to say what to write instead: for an excluded field that is the
// alternative setting rather than the canonical spelling, which the next check
// refuses anyway, and a name matching no field at all stays the decoder's to
// report.
func TestResolve_RejectsAmbiguousKeys(t *testing.T) {
	for _, tc := range []struct {
		name            string
		file            string
		flags           []string
		wantContains    []string
		wantNotContains []string
	}{{
		name:         "folded spelling",
		file:         "apiVersion: v1alpha1\nreservedcpus: \"0-3\"\n",
		wantContains: []string{"reservedcpus", "reservedCPUs"},
	}, {
		name:         "folded spelling, mixed case",
		file:         "apiVersion: v1alpha1\nReservedCPUs: \"0-3\"\n",
		wantContains: []string{"ReservedCPUs", "reservedCPUs"},
	}, {
		name:         "folded spelling, upper case",
		file:         "apiVersion: v1alpha1\nRESERVEDCPUS: \"0-3\"\n",
		wantContains: []string{"RESERVEDCPUS", "reservedCPUs"},
	}, {
		name:         "folded spelling, one letter",
		file:         "apiVersion: v1alpha1\nreservedCpus: \"0-3\"\n",
		wantContains: []string{"reservedCpus", "reservedCPUs"},
	}, {
		// Without this the file reached the precedence pass, where the delete is
		// by exact name, and overwrote the flag.
		name:         "folded spelling against an explicit flag",
		file:         "apiVersion: v1alpha1\nreservedcpus: \"2-3\"\n",
		flags:        []string{"--reserved-cpus=0-1"},
		wantContains: []string{"reservedcpus", "reservedCPUs"},
	}, {
		name:         "the same key twice",
		file:         "apiVersion: v1alpha1\nreservedCPUs: \"0-1\"\nreservedCPUs: \"2-3\"\n",
		wantContains: []string{"reservedCPUs"},
	}, {
		name:         "the same key twice, folded",
		file:         "apiVersion: v1alpha1\nreservedCPUs: \"0-1\"\nreservedcpus: \"2-3\"\n",
		wantContains: []string{"reservedcpus"},
	}, {
		name:            "folded apiVersion",
		file:            "ApiVersion: v1alpha1\nreservedCPUs: \"0-1\"\n",
		wantContains:    []string{`use "apiVersion"`},
		wantNotContains: []string{"unknown field"},
	}, {
		name:            "folded excluded field",
		file:            "apiVersion: v1alpha1\nbindaddress: \":9999\"\n",
		wantContains:    []string{"not configurable via the config file", "healthzPort"},
		wantNotContains: []string{"is spelled differently"},
	}, {
		name:            "folded excluded field, upper case",
		file:            "apiVersion: v1alpha1\nBINDADDRESS: \":9999\"\n",
		wantContains:    []string{"not configurable via the config file", "healthzPort"},
		wantNotContains: []string{"is spelled differently"},
	}, {
		name:            "folded excluded field with a flag alternative",
		file:            "apiVersion: v1alpha1\nexposepcieroots: true\n",
		wantContains:    []string{"not configurable via the config file", "args.exposePCIeRoots"},
		wantNotContains: []string{"is spelled differently"},
	}, {
		name:            "a name that matches nothing",
		file:            "apiVersion: v1alpha1\ntotallyUnknownField: 1\n",
		wantContains:    []string{"totallyUnknownField"},
		wantNotContains: []string{"spelled differently"},
	}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfgFile := writeFile(t, dir, "config.yaml", tc.file)

			var cfg driverconfig.Config
			sources := []driverconfig.Source{driverconfig.FromFile(cfgFile)}
			if len(tc.flags) > 0 {
				fs := newFlagSet(t, &cfg, tc.flags)
				sources = append(sources, driverconfig.FromFlags(fs))
			}

			_, err := driverconfig.Resolve(testr.New(t), sources)

			require.Error(t, err)
			for _, want := range tc.wantContains {
				assert.Contains(t, err.Error(), want)
			}
			for _, notWant := range tc.wantNotContains {
				assert.NotContains(t, err.Error(), notWant)
			}
		})
	}
}

// TestResolve_ReportsEveryMiscasedKey: a hand-edited file usually has more than
// one, and fixing them one restart at a time is the difference between one
// CrashLoopBackOff and three. A key misspelled rather than miscased is not this
// check's to batch, and the decoder reports those one at a time.
//
// Asserted as the whole string rather than key by key. Ranging a map named a
// different key on each node for the same ConfigMap, so the keys are sorted, and
// an exact message is what pins that ordering along with each canonical
// spelling, the separator, and the shape of the whole thing.
func TestResolve_ReportsEveryMiscasedKey(t *testing.T) {
	dir := t.TempDir()
	cfgFile := writeFile(t, dir, "config.yaml", `
apiVersion: v1alpha1
reservedcpus: "0-3"
groupby: socket
cpudevicemode: individual
`)

	_, err := driverconfig.Resolve(testr.New(t), []driverconfig.Source{
		driverconfig.FromFile(cfgFile),
	})

	require.Error(t, err)
	// we care about the inner error that the source reported, not about the
	// wrapped error Resolve() produced.
	err2 := errors.Unwrap(err)
	if err2 == nil {
		err2 = err
	}
	assert.EqualError(t, err2,
		`field "cpudevicemode" is spelled differently from the schema; use "cpuDeviceMode"; `+
			`field "groupby" is spelled differently from the schema; use "groupBy"; `+
			`field "reservedcpus" is spelled differently from the schema; use "reservedCPUs"`)
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

// TestResolve_IgnoresUnrelatedFlags: flags that are not Config fields (such as
// --config and the klog --v flag that share the process FlagSet) must not
// produce any error log, while a mapped flag set on the command line still
// overrides the file. Regression guard for the spurious "flag not found in
// flagToJSONKey" errors that used to fire on every startup with a config file.
func TestResolve_IgnoresUnrelatedFlags(t *testing.T) {
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
	fs.String("config", "", "path to the config file")
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

	got, err := driverconfig.Resolve(logger, []driverconfig.Source{
		driverconfig.FromFile(cfgFile),
		driverconfig.FromFlags(fs),
	})

	require.NoError(t, err)
	assert.NotContains(t, logs.String(), "flag not found",
		"unrelated flags must not produce an error log")
	// The mapped flag set on the command line still wins over the file value.
	assert.Equal(t, "1-2", got.ReservedCPUs)
	// A file-only field is still applied, so the assertions above cannot pass
	// without the file actually being read.
	assert.Equal(t, "from-file", got.HostnameOverride)
}

// TestWarnDeprecatedFlags_LogsWarning: a deprecated flag logs a warning naming its driverConfig replacement.
func TestWarnDeprecatedFlags_LogsWarning(t *testing.T) {
	for _, tc := range []struct {
		flag              string
		driverConfigField string
	}{
		{flag: "cpu-device-mode=individual", driverConfigField: "cpuDeviceMode"},
		{flag: "group-by=socket", driverConfigField: "groupBy"},
		{flag: "reserved-cpus=0-1", driverConfigField: "reservedCPUs"},
		{flag: "hostname-override=node1", driverConfigField: "hostnameOverride"},
		{flag: "sysfs-overlay=/tmp/overlay.yaml", driverConfigField: "sysfsOverlay"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			cfg := driverconfig.Default()
			fs := newFlagSet(t, &cfg, []string{"--" + tc.flag})

			var logs strings.Builder
			logger := funcr.New(func(prefix, args string) {
				logs.WriteString(prefix + " " + args + "\n")
			}, funcr.Options{})

			driverconfig.WarnDeprecatedFlags(fs, logger)

			assert.Contains(t, logs.String(), "deprecated")
			assert.Contains(t, logs.String(), tc.driverConfigField)
		})
	}
}

// TestWarnDeprecatedFlags_NonDeprecatedFlagNoWarning: non-deprecated flags don't trigger the deprecation warning.
func TestWarnDeprecatedFlags_NonDeprecatedFlagNoWarning(t *testing.T) {
	cfg := driverconfig.Default()
	fs := newFlagSet(t, &cfg, []string{"--bind-address=:9090"})

	var logs strings.Builder
	logger := funcr.New(func(prefix, args string) {
		logs.WriteString(prefix + " " + args + "\n")
	}, funcr.Options{})

	driverconfig.WarnDeprecatedFlags(fs, logger)

	assert.NotContains(t, logs.String(), "deprecated")
}

// deprecatedFlagNames is the hardcoded set of flags expected to be marked
// as deprecated in --help. Update it whenever a flag's deprecation status changes.
var deprecatedFlagNames = sets.New(
	"cpu-device-mode",
	"group-by",
	"reserved-cpus",
	"hostname-override",
	"sysfs-overlay",
)

// TestDeprecatedFlags_HelpTextSuffix: exactly the flags in deprecatedFlagNames
// carry a "(DEPRECATED: ...)" suffix in --help - none missing, none extra.
func TestDeprecatedFlags_HelpTextSuffix(t *testing.T) {
	cfg := driverconfig.Config{}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg.AddFlags(fs)

	fs.VisitAll(func(f *flag.Flag) {
		wantDeprecated := deprecatedFlagNames.Has(f.Name)
		isMarkedDeprecated := strings.Contains(f.Usage, "(DEPRECATED:")
		switch {
		case wantDeprecated && !isMarkedDeprecated:
			t.Errorf("flag %q is expected to be deprecated but its --help text has no DEPRECATED suffix: %q", f.Name, f.Usage)
		case !wantDeprecated && isMarkedDeprecated:
			t.Errorf("flag %q has a DEPRECATED --help suffix but isn't expected to be deprecated: %q", f.Name, f.Usage)
		}
	})
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
	// The kubelet root defaults to the standard location, so behavior is
	// unchanged unless the kubelet --root-dir is relocated.
	assert.Equal(t, driverconfig.DefaultKubeletRootDir, d.KubeletRootDir)
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
		field    string
		content  string
		wantErrs []string
	}{
		{field: "bindAddress", content: "bindAddress: \":9090\"", wantErrs: []string{"not configurable via the config file"}},
		{field: "exposePCIeRoots", content: "exposePCIeRoots: true", wantErrs: []string{"not configurable via the config file"}},
		{field: "showMetrics", content: "showMetrics: true", wantErrs: []string{"unknown field"}},
		{
			field:   "kubeletRootDir",
			content: "kubeletRootDir: /mnt/fast/k8s/kubelet",
			// Both routes, because the chart refuses the flag through extraArgs:
			// naming only the flag sends a Helm user to the one path the chart
			// rejects, and the second failure arrives a deploy later.
			wantErrs: []string{
				"not configurable via the config file",
				"use the chart's top-level kubeletRootDir value",
				"--kubelet-root-dir when running the binary directly",
			},
		},
	} {
		t.Run(tc.field, func(t *testing.T) {
			dir := t.TempDir()
			cfgFile := writeFile(t, dir, "config.yaml", "apiVersion: v1alpha1\n"+tc.content+"\n")

			_, err := driverconfig.Resolve(testr.New(t), []driverconfig.Source{
				driverconfig.FromFile(cfgFile),
			})

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.field)
			for _, want := range tc.wantErrs {
				assert.Contains(t, err.Error(), want)
			}
		})
	}
}

// The chart passes the kubelet root as a flag and renders its hostPath mounts
// from the same value, so a root that cannot be used has to fail here.
func TestResolve_KubeletRootDirFromFlag(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		wantRoot string
		wantErr  string
	}{
		{
			name:     "absolute",
			args:     []string{"--kubelet-root-dir=/mnt/fast/k8s/kubelet"},
			wantRoot: "/mnt/fast/k8s/kubelet",
		},
		{
			// Cleaned in the config layer so the logged value matches the paths
			// the driver and the chart derive from it.
			name:     "non-canonical is cleaned",
			args:     []string{"--kubelet-root-dir=/mnt/a/../kubelet//"},
			wantRoot: "/mnt/kubelet",
		},
		{
			name:    "relative",
			args:    []string{"--kubelet-root-dir=relative/kubelet"},
			wantErr: "must be an absolute path",
		},
		{
			name:    "empty",
			args:    []string{"--kubelet-root-dir="},
			wantErr: "must not be empty",
		},
		{
			// flag takes the last value, so a chart appending an empty override
			// would otherwise undo an earlier root.
			name:    "emptied after being set",
			args:    []string{"--kubelet-root-dir=/mnt/fast/k8s/kubelet", "--kubelet-root-dir="},
			wantErr: "must not be empty",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var cfg driverconfig.Config
			fs := newFlagSet(t, &cfg, tc.args)

			result, err := driverconfig.Resolve(testr.New(t), []driverconfig.Source{
				driverconfig.FromFlags(fs),
			})

			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantRoot, result.KubeletRootDir)
		})
	}
}

// TestFromFlags_BoolAndStringValues: FromFlags must produce typed values —
// bool flags as bool (not the string "true"), string flags as string, and
// custom flag.Value types (which don't implement flag.Getter) as string.
// Without this, applyMap would fail to decode a JSON string into a bool field.
func TestFromFlags_BoolAndStringValues(t *testing.T) {
	for _, tc := range []struct {
		name           string
		args           []string
		wantPCIeRoots  bool
		wantReserved   string
		wantDeviceMode string
	}{
		{
			name:           "bool flag set to true",
			args:           []string{"--expose-pcie-roots=true", "--reserved-cpus=0-3", "--cpu-device-mode=individual"},
			wantPCIeRoots:  true,
			wantReserved:   "0-3",
			wantDeviceMode: device.CPU_DEVICE_MODE_INDIVIDUAL,
		},
		{
			name:           "bool flag set to false",
			args:           []string{"--expose-pcie-roots=false", "--reserved-cpus=4-7"},
			wantPCIeRoots:  false,
			wantReserved:   "4-7",
			wantDeviceMode: device.CPU_DEVICE_MODE_GROUPED,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := driverconfig.Default()
			fs := newFlagSet(t, &cfg, tc.args)

			src := driverconfig.FromFlags(fs)
			result, err := driverconfig.Resolve(testr.New(t), []driverconfig.Source{src})

			require.NoError(t, err)
			assert.Equal(t, tc.wantPCIeRoots, result.ExposePCIeRoots)
			assert.Equal(t, tc.wantReserved, result.ReservedCPUs)
			assert.Equal(t, tc.wantDeviceMode, result.CPUDeviceMode)
		})
	}
}

func TestResolve_KubeletRootDirWithAConfigFile(t *testing.T) {
	for _, tc := range []struct {
		name             string
		content          string
		args             []string
		wantErrs         []string
		wantRoot         string
		wantReservedCPUs string
	}{
		{
			// encoding/json matches a field without regard to case, so before the
			// canonical-key pass a differently spelled key walked past the
			// exclusion and replaced a root given on the command line, which is
			// issue #231 reached through a config file.
			name:     "a differently spelled key cannot override the flag",
			content:  "kubeletrootdir: /wrong/root",
			args:     []string{"--kubelet-root-dir=/correct/root"},
			wantErrs: []string{"kubeletrootdir", "not configurable via the config file"},
		},
		{
			// The control. Refusing every file that mentions anything would close
			// the case above without the pass that closes it.
			name:             "an unrelated file leaves the flag alone",
			content:          `reservedCPUs: "0-3"`,
			args:             []string{"--kubelet-root-dir=/correct/root"},
			wantRoot:         "/correct/root",
			wantReservedCPUs: "0-3",
		},
		{
			// The file is an input, not the source: it is valid and is not allowed
			// to carry this field, so blaming its contents would send the reader
			// looking for a key that cannot be there.
			name:     "a bad flag names the file as an input",
			content:  `reservedCPUs: "0-1"`,
			args:     []string{"--kubelet-root-dir=relative/kubelet"},
			wantErrs: []string{"must be an absolute path"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cfgFile := writeFile(t, dir, "config.yaml", "apiVersion: v1alpha1\n"+tc.content+"\n")

			var cfg driverconfig.Config
			fs := newFlagSet(t, &cfg, tc.args)

			result, err := driverconfig.Resolve(testr.New(t), []driverconfig.Source{
				driverconfig.FromFile(cfgFile),
				driverconfig.FromFlags(fs),
			})

			if len(tc.wantErrs) > 0 {
				require.Error(t, err)
				for _, want := range tc.wantErrs {
					assert.Contains(t, err.Error(), want)
				}
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantRoot, result.KubeletRootDir)
			assert.Equal(t, tc.wantReservedCPUs, result.ReservedCPUs)
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
