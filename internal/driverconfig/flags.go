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

package driverconfig

import (
	"flag"
	"fmt"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
)

type FlagSource struct {
	overrides map[string]any
}

func FromFlags(flagset *flag.FlagSet) FlagSource {
	fs := FlagSource{
		overrides: make(map[string]any),
	}
	flagset.Visit(func(f *flag.Flag) {
		key, ok := flagToJSONKey[f.Name]
		if !ok {
			return
		}
		if g, ok := f.Value.(flag.Getter); ok {
			fs.overrides[key] = g.Get()
		} else {
			fs.overrides[key] = f.Value.String()
		}
	})
	return fs
}

func (fs FlagSource) Name() string {
	return "flags"
}

func (fs FlagSource) Apply(logger logr.Logger, cfg *Config) error {
	logger.V(6).Info("overrides", "stage", fs.Name(), "values", fs.overrides)
	return applyMap(cfg, fs.overrides)
}

// AddFlags registers the Config fields that are exposed as CLI flags on fs.
func (c *Config) AddFlags(fs *flag.FlagSet) {
	c.applyDefaults()

	fs.StringVar(&c.Kubeconfig, "kubeconfig", c.Kubeconfig, "absolute path to the kubeconfig file")
	fs.StringVar(&c.BindAddress, "bind-address", c.BindAddress, "The address to bind the HTTP server for /healthz and /metrics endpoints")
	fs.BoolVar(&c.ExposePCIeRoots, "expose-pcie-roots", c.ExposePCIeRoots, "Discover and expose PCIe roots as device attributes. Requires the DRAListTypeAttributes=true Feature Gate in the cluster.")
	fs.StringVar(&c.KubeletRootDir, "kubelet-root-dir", c.KubeletRootDir, "The kubelet root directory. The plugin registration and plugin data directories are derived from it as <root>/plugins_registry and <root>/plugins/<driver-name>. The Helm chart supplies this together with the matching hostPath mounts; set it only if the kubelet --root-dir is not the default /var/lib/kubelet.")
	fs.Var(newCPUDeviceModeValue(&c.CPUDeviceMode, c.CPUDeviceMode), "cpu-device-mode", deprecatedUsage("cpuDeviceMode",
		"Sets the mode for exposing CPU devices. 'grouped' exposes a single device per socket or numa node (based on --group-by). 'individual' exposes each CPU as a separate device."))
	fs.Var(newGroupByValue(&c.GroupBy, c.GroupBy), "group-by", deprecatedUsage("groupBy",
		"When --cpu-device-mode=grouped, sets the criteria for grouping CPUs. Can be set to 'socket', 'numanode', or 'machine' (machine mode requires an external scheduler to include cpuset configuration in claim allocation results)."))
	fs.StringVar(&c.ReservedCPUs, "reserved-cpus", c.ReservedCPUs, deprecatedUsage("reservedCPUs",
		"cpuset of CPUs to be excluded from ResourceSlice."))
	fs.StringVar(&c.HostnameOverride, "hostname-override", c.HostnameOverride, deprecatedUsage("hostnameOverride",
		"If non-empty, will be used as the name of the Node that kube-network-policies is running on. If unset, the node name is assumed to be the same as the node's hostname."))
	fs.StringVar(&c.SysFSOverlay, "sysfs-overlay", c.SysFSOverlay, deprecatedUsage("sysfsOverlay",
		"Path to a YAML file containing sysfs file overlays."))
}

func deprecatedUsage(driverConfigField, usage string) string {
	return fmt.Sprintf("%s (DEPRECATED: prefer driverConfig field %q; this flag will be removed in a future release)", usage, driverConfigField)
}

func (c *Config) applyDefaults() {
	defaults := Default()
	if c.BindAddress == "" {
		c.BindAddress = defaults.BindAddress
	}
	if c.CPUDeviceMode == "" {
		c.CPUDeviceMode = defaults.CPUDeviceMode
	}
	if c.GroupBy == "" {
		c.GroupBy = defaults.GroupBy
	}
	if c.KubeletRootDir == "" {
		c.KubeletRootDir = defaults.KubeletRootDir
	}
}

// cpuDeviceModeValue is a flag.Value that validates the --cpu-device-mode flag.
type cpuDeviceModeValue struct {
	value *string
}

func newCPUDeviceModeValue(val *string, def string) *cpuDeviceModeValue {
	*val = def
	return &cpuDeviceModeValue{value: val}
}

func (v *cpuDeviceModeValue) String() string {
	if v == nil || v.value == nil {
		return ""
	}
	return *v.value
}

func (v *cpuDeviceModeValue) Set(s string) error {
	if s != device.CPU_DEVICE_MODE_GROUPED && s != device.CPU_DEVICE_MODE_INDIVIDUAL {
		return fmt.Errorf("invalid value: %q, must be %s or %s", s, device.CPU_DEVICE_MODE_GROUPED, device.CPU_DEVICE_MODE_INDIVIDUAL)
	}
	*v.value = s
	return nil
}

// groupByValue is a flag.Value that validates the --group-by flag.
type groupByValue struct {
	value *string
}

func newGroupByValue(val *string, def string) *groupByValue {
	*val = def
	return &groupByValue{value: val}
}

func (v *groupByValue) String() string {
	if v == nil || v.value == nil {
		return ""
	}
	return *v.value
}

func (v *groupByValue) Set(s string) error {
	if s != device.GROUP_BY_SOCKET && s != device.GROUP_BY_NUMA_NODE && s != device.GROUP_BY_MACHINE {
		return fmt.Errorf("invalid value: %q, must be %s, %s or %s", s, device.GROUP_BY_SOCKET, device.GROUP_BY_NUMA_NODE, device.GROUP_BY_MACHINE)
	}
	*v.value = s
	return nil
}
