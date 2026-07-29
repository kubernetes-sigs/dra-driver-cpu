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
	topology "github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/resourceclaim"
	"k8s.io/utils/cpuset"
)

type External struct {
	driverName   string
	topo         *topology.CPUTopology
	onlineCPUs   cpuset.CPUSet
	reservedCPUs cpuset.CPUSet
}

func NewExternal(driverName string, topo *topology.CPUTopology, onlineCPUs, reservedCPUs cpuset.CPUSet) *External {
	return &External{
		driverName:   driverName,
		topo:         topo,
		onlineCPUs:   onlineCPUs,
		reservedCPUs: reservedCPUs,
	}
}

func (alc *External) Allocate(logger logr.Logger, availableCPUs, preferredCPUs cpuset.CPUSet, count int) (cpuset.CPUSet, error) {
	// The opaque cpuset is the preferred hint for the WHOLE request, which may
	// expand into several results (count>1). Select `count` CPUs for THIS result
	// out of the hint, restricted to what is still available for it. Selection is
	// deterministic (lowest-numbered first) so repeated Prepare calls are idempotent;
	// the specific CPUs picked per result don't matter because the driver applies
	// the union across the claim.
	candidates := preferredCPUs.Intersection(availableCPUs)
	if candidates.Size() < count {
		return cpuset.CPUSet{}, fmt.Errorf("cannot satisfy request: need %d CPUs from preferred <%s> restricted to available <%s>, only %d candidate(s)",
			count, preferredCPUs.String(), availableCPUs.String(), candidates.Size())
	}
	return cpuset.New(candidates.List()[:count]...), nil
}

func (alc *External) GetPreferredCPUs(logger logr.Logger, allocation *resourceapi.AllocationResult, alloc resourceapi.DeviceRequestAllocationResult) (cpuset.CPUSet, error) {
	preferred, ok, err := getOpaqueCPUSet(logger, alc.driverName, allocation, alloc)
	if err != nil {
		return cpuset.CPUSet{}, err
	}
	if !ok {
		return cpuset.CPUSet{}, fmt.Errorf("no opaque cpuset configuration found for allocation request %q", alloc.Request)
	}

	// A count>1 request expands into several results; the opaque cpuset must match
	// the sum of their capacities, since the driver applies the union of all results.
	total, err := requestTotalCPUs(allocation, alc.driverName, alloc.Request)
	if err != nil {
		return cpuset.CPUSet{}, err
	}
	if err := validateOpaqueCPUSet(preferred, alc.onlineCPUs, alc.reservedCPUs, total); err != nil {
		return cpuset.CPUSet{}, err
	}
	return preferred, nil
}

func (alc *External) Validate(cpus, assignedCPUs, preparedCPUs cpuset.CPUSet) error {
	// Verify cores do not overlap with other devices assigned in this same claim/batch
	if cpus.Intersection(assignedCPUs).Size() > 0 {
		return fmt.Errorf("requested CPUs %s from preferred CPUs are already assigned to another device in this claim", cpus.String())
	}

	// Verify cores do not overlap with other active claims already prepared on this node
	if cpus.Intersection(preparedCPUs).Size() > 0 {
		return fmt.Errorf("requested CPUs %s from preferred CPUs conflict with already allocated claims", cpus.String())
	}

	return nil
}

func getOpaqueCPUSet(logger logr.Logger, driverName string, allocation *resourceapi.AllocationResult, alloc resourceapi.DeviceRequestAllocationResult) (cpuset.CPUSet, bool, error) {
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

// requestTotalCPUs sums the CPU capacity consumed across every result belonging
// to the same request. A request with count>1 expands into several results, all
// sharing one request name; the opaque cpuset is sized against this total.
func requestTotalCPUs(allocation *resourceapi.AllocationResult, driverName, request string) (int, error) {
	if allocation == nil {
		return 0, fmt.Errorf("cannot compute request total: nil allocation")
	}
	total := 0
	for _, r := range allocation.Devices.Results {
		if r.Driver != driverName || r.Request != request {
			continue
		}
		q, ok := r.ConsumedCapacity[device.CPUResourceQualifiedName]
		if !ok {
			return 0, fmt.Errorf("CPU capacity %q for device %q is missing", device.CPUResourceQualifiedName, r.Device)
		}
		total += int(q.Value())
	}
	return total, nil
}

func validateOpaqueCPUSet(opaqueCPUs, onlineCPUs, reservedCPUs cpuset.CPUSet, total int) error {
	// Verify the opaque cpuset covers exactly the request's total CPU capacity.
	if opaqueCPUs.Size() != total {
		return fmt.Errorf("opaque cpuset size %d does not match the request total of %d CPUs", opaqueCPUs.Size(), total)
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
