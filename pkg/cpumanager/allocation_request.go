/*
Copyright 2025 The Kubernetes Authors.

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
	topology "github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"k8s.io/utils/cpuset"
)

// allocationRequest keeps grouped-allocation inputs explicit without
// committing to a public cross-package allocator interface yet.
type allocationRequest struct {
	topology           *topology.CPUTopology
	availableCPUs      cpuset.CPUSet
	numCPUs            int
	cpuSortingStrategy CPUSortingStrategy
	constraints        allocationConstraints
}

type allocationConstraints struct {
	preferAlignByUncoreCache bool
	cpuGroupSize             int
}

func newPackedAllocationRequest(topo *topology.CPUTopology, availableCPUs cpuset.CPUSet, numCPUs int, cpuSortingStrategy CPUSortingStrategy, preferAlignByUncoreCache bool) allocationRequest {
	return allocationRequest{
		topology:           topo,
		availableCPUs:      availableCPUs,
		numCPUs:            numCPUs,
		cpuSortingStrategy: cpuSortingStrategy,
		constraints: allocationConstraints{
			preferAlignByUncoreCache: preferAlignByUncoreCache,
		},
	}
}

func newDistributedAllocationRequest(topo *topology.CPUTopology, availableCPUs cpuset.CPUSet, numCPUs int, cpuGroupSize int, cpuSortingStrategy CPUSortingStrategy) allocationRequest {
	return allocationRequest{
		topology:           topo,
		availableCPUs:      availableCPUs,
		numCPUs:            numCPUs,
		cpuSortingStrategy: cpuSortingStrategy,
		constraints: allocationConstraints{
			cpuGroupSize: cpuGroupSize,
		},
	}
}
