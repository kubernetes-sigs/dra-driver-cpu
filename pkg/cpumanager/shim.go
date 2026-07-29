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

package cpumanager

import (
	"fmt"

	"github.com/go-logr/logr"
	topology "github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/utils/cpuset"
)

type Allocator struct {
	driverName string
	topo       *topology.CPUTopology
}

func NewAllocator(driverName string, topo *topology.CPUTopology) *Allocator {
	return &Allocator{
		driverName: driverName,
		topo:       topo,
	}
}

func (alc *Allocator) Allocate(logger logr.Logger, availableCPUs, preferredCPUs cpuset.CPUSet, count int) (cpuset.CPUSet, error) {
	return TakeByTopologyNUMAPacked(logger, alc.topo, availableCPUs, count, CPUSortingStrategyPacked, true)
}

func (alc *Allocator) GetPreferredCPUs(logger logr.Logger, allocation *resourceapi.AllocationResult, alloc resourceapi.DeviceRequestAllocationResult, count int) (cpuset.CPUSet, error) {
	if allocation == nil {
		return cpuset.New(), nil
	}
	for _, config := range allocation.Devices.Config {
		if config.Opaque == nil || config.Opaque.Driver != alc.driverName {
			continue
		}
		return cpuset.New(), fmt.Errorf("opaque device configuration is not supported by the %q allocator; use the external allocator to provide custom cpuset assignments", alc.driverName)
	}
	return cpuset.New(), nil
}

func (alc *Allocator) Validate(preferredCPUs, preparedCPUs, assignedCPUs cpuset.CPUSet) error {
	return nil // we trust the cpumanager code
}
