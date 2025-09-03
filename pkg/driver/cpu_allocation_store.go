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

package driver

import (
	"sync"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/cpuset"
)

// CPUAllocationStore is the single source of truth for CPU allocations.
type CPUAllocationStore struct {
	mu                       sync.RWMutex
	availableCPUs            cpuset.CPUSet
	reservedCPUs             cpuset.CPUSet
	resourceClaimAllocations map[types.UID]cpuset.CPUSet
}

// NewCPUAllocationStore creates a new CPUAllocationStore.
func NewCPUAllocationStore(provider CPUInfoProvider, reservedCPUs cpuset.CPUSet) *CPUAllocationStore {
	cpuIDs := []int{}
	cpuInfo, err := provider.GetCPUInfos()
	if err != nil {
		klog.Fatalf("Fatal error getting CPU topology: %v", err)
	}
	for _, cpu := range cpuInfo {
		cpuIDs = append(cpuIDs, cpu.CpuID)
	}
	allCPUsSet := cpuset.New(cpuIDs...)
	availableCPUs := allCPUsSet.Difference(reservedCPUs)

	return &CPUAllocationStore{
		availableCPUs:            availableCPUs,
		reservedCPUs:             reservedCPUs,
		resourceClaimAllocations: make(map[types.UID]cpuset.CPUSet),
	}
}

// AddResourceClaimAllocation adds a new resource claim allocation to the store.
func (s *CPUAllocationStore) AddResourceClaimAllocation(claimUID types.UID, cpus cpuset.CPUSet) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resourceClaimAllocations[claimUID] = cpus
	klog.Infof("Added allocation for resource claim %s: CPUs %s", claimUID, cpus.String())
}

// RemoveResourceClaimAllocation removes a resource claim allocation from the store.
func (s *CPUAllocationStore) RemoveResourceClaimAllocation(claimUID types.UID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.resourceClaimAllocations[claimUID]; ok {
		delete(s.resourceClaimAllocations, claimUID)
		klog.Infof("Removed allocation for resource claim %s", claimUID)
	}
}

// GetSharedCPUs calculates and returns the set of CPUs not reserved by any resource claim.
func (s *CPUAllocationStore) GetSharedCPUs() cpuset.CPUSet {
	s.mu.RLock()
	defer s.mu.RUnlock()

	allocatedCPUs := cpuset.New()
	for _, cpus := range s.resourceClaimAllocations {
		allocatedCPUs = allocatedCPUs.Union(cpus)
	}
	return s.availableCPUs.Difference(allocatedCPUs)
}
