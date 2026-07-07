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

package cpuallocator

import (
	"fmt"

	"github.com/go-logr/logr"
	opaqueapi "github.com/kubernetes-sigs/dra-driver-cpu/api"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/resourceclaim"
	"k8s.io/utils/cpuset"
)

func ValidateOpaqueCPUSet(allocatedCPUs, opaqueCPUSet, cpuAssignment cpuset.CPUSet, claimCPUCount int, onlineCPUs, reservedCPUs cpuset.CPUSet) error {
	// Verify core count matches requested capacity
	if opaqueCPUSet.Size() != claimCPUCount {
		return fmt.Errorf("opaque config cpuset size %d does not match requested capacity %d", opaqueCPUSet.Size(), claimCPUCount)
	}

	// Verify CPUs are online
	if !opaqueCPUSet.IsSubsetOf(onlineCPUs) {
		offlineCPUs := opaqueCPUSet.Difference(onlineCPUs)
		return fmt.Errorf("requested CPUs %s from opaque config contain offline cores: %s", opaqueCPUSet.String(), offlineCPUs.String())
	}

	// Verify CPUs are not part of --reserved-cpus config passed to the driver
	reservedOverlap := opaqueCPUSet.Intersection(reservedCPUs)
	if reservedOverlap.Size() > 0 {
		return fmt.Errorf("requested CPUs %s from opaque config contain reserved cores: %s", opaqueCPUSet.String(), reservedOverlap.String())
	}

	// Verify cores do not overlap with other claims prepared in this same batch
	currentClaimCPUs := opaqueCPUSet.Intersection(cpuAssignment)
	if currentClaimCPUs.Size() > 0 {
		return fmt.Errorf("requested CPUs %s from opaque config are already assigned to another device in this claim", opaqueCPUSet.String())
	}

	// Verify cores do not overlap with other active claims on this node
	existingClaimCPUs := allocatedCPUs
	if opaqueCPUSet.Intersection(existingClaimCPUs).Size() > 0 {
		return fmt.Errorf("requested CPUs %s from opaque config conflict with already allocated claims", opaqueCPUSet.String())
	}

	return nil
}

func GetOpaqueCPUSet(logger logr.Logger, driverName string, allocation *resourceapi.AllocationResult, alloc resourceapi.DeviceRequestAllocationResult) (cpuset.CPUSet, bool, error) {
	if allocation == nil {
		return cpuset.CPUSet{}, false, nil
	}

	// ConfigForResult applies the DRA spec matching rules: an empty 'requests' field
	// targets all requests, and a parent request name also targets its subrequests.
	configs := resourceclaim.ConfigForResult(allocation.Devices.Config, alloc)
	var matchedConfig *resourceapi.DeviceAllocationConfiguration
	matchCount := 0

	for i := range configs {
		config := &configs[i]
		if config.Opaque.Driver != driverName {
			continue
		}
		if config.Source != resourceapi.AllocationConfigSourceClaim {
			return cpuset.CPUSet{}, false, fmt.Errorf("opaque config: configuration from DeviceClass is not supported by this driver, custom cpusets must be defined per ResourceClaim request")
		}
		matchedConfig = config
		matchCount++
	}

	if matchCount != 1 {
		return cpuset.CPUSet{}, false, fmt.Errorf("opaque config: request %q is targeted by %d configurations, must be targeted by exactly 1", alloc.Request, matchCount)
	}

	// Return the matched config if found
	if matchedConfig != nil && len(matchedConfig.Opaque.Parameters.Raw) > 0 {
		parsedCPUSet, err := opaqueapi.ParseOpaqueConfig(matchedConfig.Opaque.Parameters.Raw)
		if err != nil {
			return cpuset.CPUSet{}, false, err
		}
		logger.V(4).Info("found cpuset override in opaque config", "request", alloc.Request, "cpuset", parsedCPUSet.String())
		return parsedCPUSet, true, nil
	}

	return cpuset.CPUSet{}, false, nil
}
