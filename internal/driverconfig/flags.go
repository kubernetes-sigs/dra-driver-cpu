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

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/driver"
)

// AddFlags registers every Config field as a CLI flag on fs.
func (c *Config) AddFlags(fs *flag.FlagSet) {
	c.applyDefaults()

	fs.StringVar(&c.Kubeconfig, "kubeconfig", c.Kubeconfig, "absolute path to the kubeconfig file")
	fs.StringVar(&c.HostnameOverride, "hostname-override", c.HostnameOverride, "If non-empty, will be used as the name of the Node that kube-network-policies is running on. If unset, the node name is assumed to be the same as the node's hostname.")
	fs.StringVar(&c.BindAddress, "bind-address", c.BindAddress, "The address to bind the HTTP server for /healthz and /metrics endpoints")
	fs.StringVar(&c.ReservedCPUs, "reserved-cpus", c.ReservedCPUs, "cpuset of CPUs to be excluded from ResourceSlice.")
	fs.Var(newCPUDeviceModeValue(&c.CPUDeviceMode, c.CPUDeviceMode), "cpu-device-mode", "Sets the mode for exposing CPU devices. 'grouped' exposes a single device per socket or numa node (based on --group-by). 'individual' exposes each CPU as a separate device.")
	fs.Var(newGroupByValue(&c.GroupBy, c.GroupBy), "group-by", "When --cpu-device-mode=grouped, sets the criteria for grouping CPUs. Can be set to 'socket', 'numanode', or 'machine' (machine mode requires an external scheduler to include cpuset configuration in claim allocation results).")
	fs.BoolVar(&c.ExposePCIeRoots, "expose-pcie-roots", c.ExposePCIeRoots, "Discover and expose PCIe roots as device attributes. Requires the DRAListTypeAttributes=true Feature Gate in the cluster.")
	fs.BoolVar(&c.ShowMetrics, "show-metrics", c.ShowMetrics, "Print custom driver metrics metadata as JSON and exit.")
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
}

// Validate checks that enum fields hold recognised values.
// Config files bypass the flag.Value validators, so Load calls this after merging.
func (c Config) Validate() error {
	if c.CPUDeviceMode != driver.CPU_DEVICE_MODE_GROUPED && c.CPUDeviceMode != driver.CPU_DEVICE_MODE_INDIVIDUAL {
		return fmt.Errorf("invalid cpuDeviceMode %q: must be %q or %q",
			c.CPUDeviceMode, driver.CPU_DEVICE_MODE_GROUPED, driver.CPU_DEVICE_MODE_INDIVIDUAL)
	}
	if c.CPUDeviceMode == driver.CPU_DEVICE_MODE_GROUPED {
		if c.GroupBy != driver.GROUP_BY_SOCKET && c.GroupBy != driver.GROUP_BY_NUMA_NODE && c.GroupBy != driver.GROUP_BY_MACHINE {
			return fmt.Errorf("invalid groupBy %q: must be %q, %q, or %q",
				c.GroupBy, driver.GROUP_BY_SOCKET, driver.GROUP_BY_NUMA_NODE, driver.GROUP_BY_MACHINE)
		}
	}
	return nil
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
	if s != driver.CPU_DEVICE_MODE_GROUPED && s != driver.CPU_DEVICE_MODE_INDIVIDUAL {
		return fmt.Errorf("invalid value: %q, must be %s or %s", s, driver.CPU_DEVICE_MODE_GROUPED, driver.CPU_DEVICE_MODE_INDIVIDUAL)
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
	if s != driver.GROUP_BY_SOCKET && s != driver.GROUP_BY_NUMA_NODE && s != driver.GROUP_BY_MACHINE {
		return fmt.Errorf("invalid value: %q, must be %s, %s or %s", s, driver.GROUP_BY_SOCKET, driver.GROUP_BY_NUMA_NODE, driver.GROUP_BY_MACHINE)
	}
	*v.value = s
	return nil
}
