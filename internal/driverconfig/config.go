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

// ConfigAPIVersion is the version validated in config files.
const ConfigAPIVersion = "v1alpha1"

// Config holds the driver runtime configuration.
type Config struct {
	Kubeconfig       string `json:"kubeconfig,omitempty"`
	HostnameOverride string `json:"hostnameOverride,omitempty"`
	BindAddress      string `json:"bindAddress,omitempty"`
	ReservedCPUs     string `json:"reservedCPUs,omitempty"`
	CPUDeviceMode    string `json:"cpuDeviceMode"`
	GroupBy          string `json:"groupBy,omitempty"`
	ExposePCIeRoots  bool   `json:"exposePCIeRoots,omitempty"`
	ShowMetrics      bool   `json:"showMetrics,omitempty"`
}
