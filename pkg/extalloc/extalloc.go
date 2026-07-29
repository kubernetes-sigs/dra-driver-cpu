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

package extalloc

import (
	"fmt"

	"github.com/go-logr/logr"
	opaqueapi "github.com/kubernetes-sigs/dra-driver-cpu/api"
	topology "github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/resourceclaim"
	"k8s.io/utils/cpuset"
)

type Allocator struct {
	driverName   string
	topo         *topology.CPUTopology
	onlineCPUs   cpuset.CPUSet
	reservedCPUs cpuset.CPUSet
}

func NewAllocator(driverName string, topo *topology.CPUTopology, onlineCPUs, reservedCPUs cpuset.CPUSet) *Allocator {
	return &Allocator{
		driverName:   driverName,
		topo:         topo,
		onlineCPUs:   onlineCPUs,
		reservedCPUs: reservedCPUs,
	}
}

func (alc *Allocator) Allocate(logger logr.Logger, availableCPUs, preferredCPUs cpuset.CPUSet, count int) (cpuset.CPUSet, error) {
	if !preferredCPUs.IsSubsetOf(availableCPUs) {
		return cpuset.CPUSet{}, fmt.Errorf("preferred CPUs <%s> must be a subset of available CPUs <%s>", preferredCPUs.String(), availableCPUs.String())
	}
	return preferredCPUs, nil
}

func (alc *Allocator) GetPreferredCPUs(logger logr.Logger, allocation *resourceapi.AllocationResult, alloc resourceapi.DeviceRequestAllocationResult, count int) (cpuset.CPUSet, error) {
	preferred, _, err := GetOpaqueCPUSet(logger, alc.driverName, allocation, alloc)
	if err != nil {
		return cpuset.CPUSet{}, err
	}
	err = ValidateOpaqueCPUSet(preferred, alc.onlineCPUs, alc.reservedCPUs, count)
	if err != nil {
		return cpuset.CPUSet{}, err
	}
	return preferred, nil
}

func (alc *Allocator) Validate(preferredCPUs, preparedCPUs, assignedCPUs cpuset.CPUSet) error {
	// Verify cores do not overlap with other devices assigned in this same claim/batch
	if preferredCPUs.Intersection(assignedCPUs).Size() > 0 {
		return fmt.Errorf("requested CPUs %s from preferred CPUs are already assigned to another device in this claim", preferredCPUs.String())
	}

	// Verify cores do not overlap with other active claims already prepared on this node
	if preferredCPUs.Intersection(preparedCPUs).Size() > 0 {
		return fmt.Errorf("requested CPUs %s from preferred CPUs conflict with already allocated claims", preferredCPUs.String())
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
		logger.V(4).Info("found cpuset override in opaque CPU set", "request", alloc.Request, "cpuset", parsedCPUSet.String())
		return parsedCPUSet, true, nil
	}

	return cpuset.CPUSet{}, false, nil
}

func ValidateOpaqueCPUSet(opaqueCPUs, onlineCPUs, reservedCPUs cpuset.CPUSet, count int) error {
	// Verify core count matches requested capacity
	if opaqueCPUs.Size() != count {
		return fmt.Errorf("preferred CPUs cpuset size %d does not match requested capacity %d", opaqueCPUs.Size(), count)
	}

	// Verify CPUs are online
	if !opaqueCPUs.IsSubsetOf(onlineCPUs) {
		offlineCPUs := opaqueCPUs.Difference(onlineCPUs)
		return fmt.Errorf("requested CPUs %s from preferred CPUs contain offline cores: %s", opaqueCPUs.String(), offlineCPUs.String())
	}

	// Verify CPUs are not part of --reserved-cpus config passed to the driver
	reservedOverlap := opaqueCPUs.Intersection(reservedCPUs)
	if reservedOverlap.Size() > 0 {
		return fmt.Errorf("requested CPUs %s from preferred CPUs contain reserved cores: %s", opaqueCPUs.String(), reservedOverlap.String())
	}

	return nil
}
