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

package v1alpha1

const (
	APIVersion = "v1alpha1"
)

// OpaqueConfig is the versioned configuration schema for v1alpha1.
//
// This struct defines the schema of the custom allocation configuration that goes into
// the ResourceClaim status at: claim.Status.Allocation.Devices.Config[*].Opaque.Parameters.Raw.
//
// This configuration is opaque to Kubernetes, which passes it directly as raw JSON bytes to be
// parsed by this driver.
type OpaqueConfig struct {
	// APIVersion specifies the schema version. Should be "v1alpha1".
	APIVersion string `json:"apiVersion"`
	// CPUConfig holds CPU-specific configuration settings.
	CPUConfig CPUConfig `json:"cpuConfig"`
}

// CPUConfig holds CPU-specific configuration settings.
type CPUConfig struct {
	// CPUSet specifies the cpus to allocate in standard cpuset syntax (e.g., "0-3,5").
	CPUSet string `json:"cpuset,omitempty"`
}
