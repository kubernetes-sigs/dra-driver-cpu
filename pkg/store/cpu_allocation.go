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

package store

import (
	"sync"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/cpuset"
)

// CPUAllocation is the single source of truth for CPU allocations.
type CPUAllocation struct {
	mu                       sync.RWMutex
	availableCPUs            cpuset.CPUSet
	reservedCPUs             cpuset.CPUSet
	resourceClaimAllocations map[types.UID]cpuset.CPUSet
}

// NewCPUAllocation creates a new CPUAllocation.
func NewCPUAllocation(cpuTopology *cpuinfo.CPUTopology, reservedCPUs cpuset.CPUSet) *CPUAllocation {
	cpuIDs := []int{}
	for cpuID := range cpuTopology.CPUDetails {
		cpuIDs = append(cpuIDs, cpuID)
	}
	allCPUsSet := cpuset.New(cpuIDs...)
	availableCPUs := allCPUsSet.Difference(reservedCPUs)

	return &CPUAllocation{
		availableCPUs:            availableCPUs,
		reservedCPUs:             reservedCPUs,
		resourceClaimAllocations: make(map[types.UID]cpuset.CPUSet),
	}
}

// AddResourceClaimAllocation adds a new resource claim allocation to the store.
// TODO(pravk03): Keep track of all allocated CPUs here so that GetSharedCPUs() can return in O(1).
func (s *CPUAllocation) AddResourceClaimAllocation(claimUID types.UID, cpus cpuset.CPUSet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resourceClaimAllocations[claimUID] = cpus
	klog.Infof("Added allocation for resource claim %s: CPUs %s", claimUID, cpus.String())
}

// RemoveResourceClaimAllocation removes a resource claim allocation from the store.
func (s *CPUAllocation) RemoveResourceClaimAllocation(claimUID types.UID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.resourceClaimAllocations[claimUID]; ok {
		delete(s.resourceClaimAllocations, claimUID)
		klog.Infof("Removed allocation for resource claim %s", claimUID)
	}
}

// GetSharedCPUs calculates and returns the set of CPUs not reserved by any resource claim.
func (s *CPUAllocation) GetSharedCPUs() cpuset.CPUSet {
	s.mu.RLock()
	defer s.mu.RUnlock()

	allocatedCPUs := cpuset.New()
	for _, cpus := range s.resourceClaimAllocations {
		allocatedCPUs = allocatedCPUs.Union(cpus)
	}
	return s.availableCPUs.Difference(allocatedCPUs)
}

// GetResourceClaimAllocation returns the cpuset for a given resource claim.
func (s *CPUAllocation) GetResourceClaimAllocation(claimUID types.UID) (cpuset.CPUSet, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cpus, ok := s.resourceClaimAllocations[claimUID]
	return cpus, ok
}
