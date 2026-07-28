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
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/cpuset"
)

type claimAllocation struct {
	cpus     cpuset.CPUSet
	enforced bool
}

// CPUAllocation is the single source of truth for CPU allocations.
type CPUAllocation struct {
	mu                       sync.RWMutex
	availableCPUs            cpuset.CPUSet
	reservedCPUs             cpuset.CPUSet
	resourceClaimAllocations map[types.UID]claimAllocation
	preparedCPUs             cpuset.CPUSet
	enforcedCPUs             cpuset.CPUSet
}

// AllocationSnapshot is a point-in-time summary of CPU allocation state.
type AllocationSnapshot struct {
	AllocatedCPUs        int
	AvailableCPUs        int
	ReservedCPUs         int
	ActiveResourceClaims int
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
		resourceClaimAllocations: make(map[types.UID]claimAllocation),
		preparedCPUs:             cpuset.New(),
		enforcedCPUs:             cpuset.New(),
	}
}

// ReserveResourceClaimAllocation records a prepared claim without removing its CPUs from
// shared containers. The CPUs remain unavailable to other exclusive claims until Unprepare.
func (s *CPUAllocation) ReserveResourceClaimAllocation(logger logr.Logger, claimUID types.UID, cpus cpuset.CPUSet) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if allocation, ok := s.resourceClaimAllocations[claimUID]; ok {
		if allocation.cpus.Equals(cpus) {
			return nil
		}
		return fmt.Errorf("claim %q is already prepared with CPUs %q (requested %q)", claimUID, allocation.cpus.String(), cpus.String())
	}
	if !cpus.IsSubsetOf(s.availableCPUs.Difference(s.preparedCPUs)) {
		return fmt.Errorf("claim %q has overlapping CPU assignment %q", claimUID, cpus.String())
	}
	s.resourceClaimAllocations[claimUID] = claimAllocation{cpus: cpus}
	s.preparedCPUs = s.preparedCPUs.Union(cpus)
	logger.Info("reserved allocation for resource claim", "cpus", cpus.String())
	return nil
}

// EnforceResourceClaims validates and marks a container's claims as enforced.
func (s *CPUAllocation) EnforceResourceClaims(expected map[types.UID]cpuset.CPUSet) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	claimUIDs := make([]types.UID, 0, len(expected))
	for claimUID, cpus := range expected {
		allocation, ok := s.resourceClaimAllocations[claimUID]
		if !ok {
			return fmt.Errorf("claim %q is not prepared by this driver", claimUID)
		}
		if !allocation.cpus.Equals(cpus) {
			return fmt.Errorf("validation failed for claim %q: cpuset mismatch (expected %q, got %q)", claimUID, allocation.cpus.String(), cpus.String())
		}
	}

	for claimUID := range expected {
		claimUIDs = append(claimUIDs, claimUID)
	}
	s.setResourceClaimsEnforcementStateLocked(true, claimUIDs...)
	return nil
}

// SetResourceClaimsEnforcementState updates whether prepared claims are enforced by a running container.
func (s *CPUAllocation) SetResourceClaimsEnforcementState(enforced bool, claimUIDs ...types.UID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, claimUID := range claimUIDs {
		if _, ok := s.resourceClaimAllocations[claimUID]; !ok {
			return fmt.Errorf("claim %q is not prepared by this driver", claimUID)
		}
	}
	s.setResourceClaimsEnforcementStateLocked(enforced, claimUIDs...)
	return nil
}

func (s *CPUAllocation) setResourceClaimsEnforcementStateLocked(enforced bool, claimUIDs ...types.UID) {
	for _, claimUID := range claimUIDs {
		allocation := s.resourceClaimAllocations[claimUID]
		if allocation.enforced == enforced {
			continue
		}
		allocation.enforced = enforced
		s.resourceClaimAllocations[claimUID] = allocation
		if enforced {
			s.enforcedCPUs = s.enforcedCPUs.Union(allocation.cpus)
		} else {
			s.enforcedCPUs = s.enforcedCPUs.Difference(allocation.cpus)
		}
	}
}

// RemoveResourceClaimAllocation removes a resource claim allocation from the store.
func (s *CPUAllocation) RemoveResourceClaimAllocation(logger logr.Logger, claimUID types.UID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.resourceClaimAllocations[claimUID]; ok {
		s.removeLocked(claimUID)
		logger.Info("removed allocation for resource claim")
	}
}

func (s *CPUAllocation) removeLocked(claimUID types.UID) {
	allocation, ok := s.resourceClaimAllocations[claimUID]
	if !ok {
		return
	}
	delete(s.resourceClaimAllocations, claimUID)
	s.preparedCPUs = s.preparedCPUs.Difference(allocation.cpus)
	if allocation.enforced {
		s.enforcedCPUs = s.enforcedCPUs.Difference(allocation.cpus)
	}
}

// GetSharedCPUs returns CPUs available to shared containers. Prepared claims only
// leave this pool while they are enforced by a live container.
func (s *CPUAllocation) GetSharedCPUs() cpuset.CPUSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.availableCPUs.Difference(s.enforcedCPUs)
}

// GetAllocatableCPUs returns CPUs available for a new exclusive claim.
func (s *CPUAllocation) GetAllocatableCPUs() cpuset.CPUSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.availableCPUs.Difference(s.preparedCPUs)
}

// GetResourceClaimAllocation returns the cpuset for a given resource claim.
func (s *CPUAllocation) GetResourceClaimAllocation(claimUID types.UID) (cpuset.CPUSet, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	allocation, ok := s.resourceClaimAllocations[claimUID]
	return allocation.cpus, ok
}

// GetReservedCPUs returns the set of reserved CPUs.
func (s *CPUAllocation) GetReservedCPUs() cpuset.CPUSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reservedCPUs
}

// GetPreparedCPUs returns the CPUs reserved for prepared claims.
func (s *CPUAllocation) GetPreparedCPUs() cpuset.CPUSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.preparedCPUs
}

// Snapshot returns a point-in-time summary of CPU allocation state.
func (s *CPUAllocation) Snapshot() AllocationSnapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return AllocationSnapshot{
		AllocatedCPUs:        s.preparedCPUs.Size(),
		AvailableCPUs:        s.availableCPUs.Difference(s.preparedCPUs).Size(),
		ReservedCPUs:         s.reservedCPUs.Size(),
		ActiveResourceClaims: len(s.resourceClaimAllocations),
	}
}
