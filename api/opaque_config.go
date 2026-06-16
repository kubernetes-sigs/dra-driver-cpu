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

package api

import (
	"encoding/json"
	"fmt"

	"github.com/kubernetes-sigs/dra-driver-cpu/api/v1alpha1"
	"k8s.io/utils/cpuset"
)

// ParseOpaqueConfig decodes the raw opaque parameters into the driver's internal format
// based on the apiVersion.
func ParseOpaqueConfig(raw []byte) (cpuset.CPUSet, error) {
	var config v1alpha1.OpaqueConfig
	if err := json.Unmarshal(raw, &config); err != nil {
		return cpuset.CPUSet{}, fmt.Errorf("failed to unmarshal opaque config: %w", err)
	}

	switch config.APIVersion {
	case v1alpha1.APIVersion:
		if config.CPUConfig.CPUSet == "" {
			return cpuset.CPUSet{}, fmt.Errorf("opaque config: cpuConfig.cpuset is empty or missing")
		}
		parsedSet, err := cpuset.Parse(config.CPUConfig.CPUSet)
		if err != nil {
			return cpuset.CPUSet{}, fmt.Errorf("opaque config: failed to parse cpuset %q: %w", config.CPUConfig.CPUSet, err)
		}
		return parsedSet, nil

	default:
		return cpuset.CPUSet{}, fmt.Errorf("unsupported opaque config apiVersion: %q", config.APIVersion)
	}
}
