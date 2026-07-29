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
	topology "github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpumanager"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/resourceclaim"
	"k8s.io/utils/cpuset"
)

type Kubelet struct {
	driverName string
	topo       *topology.CPUTopology
}

func NewKubelet(driverName string, topo *topology.CPUTopology) *Kubelet {
	return &Kubelet{
		driverName: driverName,
		topo:       topo,
	}
}

func (alc *Kubelet) Allocate(logger logr.Logger, availableCPUs, preferredCPUs cpuset.CPUSet, count int) (cpuset.CPUSet, error) {
	if preferredCPUs.Size() == 0 {
		return cpumanager.TakeByTopologyNUMAPacked(logger, alc.topo, availableCPUs, count, cpumanager.CPUSortingStrategyPacked, true)
	}
	priorityCPUs := preferredCPUs.Intersection(availableCPUs)
	if priorityCPUs.Size() >= count {
		return cpumanager.TakeByTopologyNUMAPacked(logger, alc.topo, priorityCPUs, count, cpumanager.CPUSortingStrategyPacked, true)
	}
	alloc, err := cpumanager.TakeByTopologyNUMAPacked(logger, alc.topo, priorityCPUs, priorityCPUs.Size(), cpumanager.CPUSortingStrategyPacked, true)
	if err != nil {
		return cpuset.New(), fmt.Errorf("allocating %d CPUs from the priority set %v: %w", priorityCPUs.Size(), priorityCPUs, err)
	}
	remainingCPUs := availableCPUs.Difference(alloc)
	restCount := count - alloc.Size()
	rest, err := cpumanager.TakeByTopologyNUMAPacked(logger, alc.topo, remainingCPUs, restCount, cpumanager.CPUSortingStrategyPacked, true)
	if err != nil {
		return cpuset.New(), fmt.Errorf("allocating %d CPUs from the remaining set %v: %w", restCount, remainingCPUs, err)
	}
	return alloc.Union(rest), nil
}

func (alc *Kubelet) GetPreferredCPUs(logger logr.Logger, allocation *resourceapi.AllocationResult, alloc resourceapi.DeviceRequestAllocationResult) (cpuset.CPUSet, error) {
	if allocation == nil {
		return cpuset.New(), nil
	}

	for _, config := range resourceclaim.ConfigForResult(allocation.Devices.Config, alloc) {
		if config.Opaque == nil || config.Opaque.Driver != alc.driverName {
			continue
		}
		return cpuset.New(), ErrUnsupportedPreferredCPUs
	}
	return cpuset.New(), nil
}

func (alc *Kubelet) Validate(_, _, _ cpuset.CPUSet) error {
	return nil // we trust the cpumanager code
}
