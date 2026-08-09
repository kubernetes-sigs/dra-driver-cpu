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
	"fmt"

	"sigs.k8s.io/yaml"
)

// ConfigAPIVersion is the version validated in config files.
const ConfigAPIVersion = "v1alpha1"

// Config holds the driver runtime configuration.
type Config struct {
	Kubeconfig       string `json:"kubeconfig,omitempty"`
	HostnameOverride string `json:"hostnameOverride,omitempty"`
	BindAddress      string `json:"bindAddress,omitempty"`
	ReservedCPUs     string `json:"reservedCPUs,omitempty"`
	CPUDeviceMode    string `json:"cpuDeviceMode,omitempty"`
	GroupBy          string `json:"groupBy,omitempty"`
	ExposePCIeRoots  bool   `json:"exposePCIeRoots,omitempty"`
	SysFSOverlay     string `json:"sysfsOverlay,omitempty"`
	// KubeletRootDir is the kubelet root directory. The plugin registration and
	// plugins directories are derived from it as <root>/plugins_registry and
	// <root>/plugins. Set it when the kubelet --root-dir is not the default
	// /var/lib/kubelet.
	KubeletRootDir string `json:"kubeletRootDir,omitempty"`
}

// LogValues returns key-value pairs for structured logging of the config.
func (c Config) LogValues() []any {
	return []any{
		"kubeconfig", c.Kubeconfig,
		"bindAddress", c.BindAddress,
		"cpuDeviceMode", c.CPUDeviceMode,
		"groupBy", c.GroupBy,
		"reservedCPUs", c.ReservedCPUs,
		"hostnameOverride", c.HostnameOverride,
		"exposePCIeRoots", c.ExposePCIeRoots,
		"sysfsOverlay", c.SysFSOverlay,
		"kubeletRootDir", c.KubeletRootDir,
	}
}

// dumpConfig mirrors Config field-for-field but drops the omitempty json
// tags, so Dump also prints zero values (e.g. exposePCIeRoots=false).
type dumpConfig struct {
	Kubeconfig       string `json:"kubeconfig"`
	HostnameOverride string `json:"hostnameOverride"`
	BindAddress      string `json:"bindAddress"`
	ReservedCPUs     string `json:"reservedCPUs"`
	CPUDeviceMode    string `json:"cpuDeviceMode"`
	GroupBy          string `json:"groupBy"`
	ExposePCIeRoots  bool   `json:"exposePCIeRoots"`
	SysFSOverlay     string `json:"sysfsOverlay"`
	KubeletRootDir   string `json:"kubeletRootDir"`
}

// Dump renders the Config as YAML, for logging a human-readable snapshot of
// the fully loaded configuration. Zero values are included, unlike
// marshalling Config directly, since they reflect real runtime state.
func (c Config) Dump() string {
	out, err := yaml.Marshal(dumpConfig(c))
	if err != nil {
		return fmt.Sprintf("<!!! FAILED TO MARSHAL Config: %v !!!>", err)
	}
	return string(out)
}
