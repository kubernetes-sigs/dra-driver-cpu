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
	"github.com/go-logr/logr"
	topology "github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpumanager"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/resourceclaim"
	"k8s.io/utils/cpuset"
)

// CPUManager allocates CPUs using the same algorithms implemented in
// the kubelet cpumanager when using its static policy.
// This allocator implementations does not support any external
// hint from opaque configuration; if hints are detected, the
// allocator will always hard fail.
// See the code in `pkg/cpumanager` for details.
type CPUManager struct {
	driverName string
	topo       *topology.CPUTopology
}

func NewCPUManager(driverName string, topo *topology.CPUTopology) *CPUManager {
	return &CPUManager{
		driverName: driverName,
		topo:       topo,
	}
}

func (alc *CPUManager) Allocate(logger logr.Logger, availableCPUs, _ cpuset.CPUSet, count int) (cpuset.CPUSet, error) {
	// Allocate is _not_ interface compliant by design because this shim will always hard-reject
	// any preferred CPUs coming from opaque configuration.
	// Therefore we can safely assume the preferred CPU set is always empty and simplify accordingly.
	return cpumanager.TakeByTopologyNUMAPacked(logger, alc.topo, availableCPUs, count, cpumanager.CPUSortingStrategyPacked, true)
}

func (alc *CPUManager) GetPreferredCPUs(logger logr.Logger, allocation *resourceapi.AllocationResult, alloc resourceapi.DeviceRequestAllocationResult) (cpuset.CPUSet, error) {
	if allocation == nil {
		return cpuset.New(), nil
	}
	// external opaque hints are not supported and must be rejected with the sentinel error.
	for _, config := range resourceclaim.ConfigForResult(allocation.Devices.Config, alloc) {
		if config.Opaque == nil || config.Opaque.Driver != alc.driverName {
			continue
		}
		return cpuset.New(), ErrUnsupportedPreferredCPUs
	}
	return cpuset.New(), nil
}

func (alc *CPUManager) Validate(_, _, _ cpuset.CPUSet) error {
	return nil // we trust the cpumanager code
}
