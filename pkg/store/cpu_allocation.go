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
	"errors"
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/cpuset"
)

type AlreadyOwned struct {
	ClaimUID types.UID
	Owner    OwnerIdent
}

func (ao AlreadyOwned) Error() string {
	return fmt.Sprintf("claimUID %q already bound to pod %q container %q", ao.ClaimUID, ao.Owner.PodUID, ao.Owner.ContainerName)
}

type OwnerIdent struct {
	PodUID        types.UID
	ContainerName string
}

func (oi OwnerIdent) Vacant() bool {
	return oi.PodUID == types.UID("") && oi.ContainerName == ""
}

func (oi OwnerIdent) Equal(podUID types.UID, containerName string) bool {
	return oi.PodUID == podUID && oi.ContainerName == containerName
}

func (oi OwnerIdent) EqualIdent(x OwnerIdent) bool {
	return oi.PodUID == x.PodUID && oi.ContainerName == x.ContainerName
}

type claimAllocation struct {
	cpus  cpuset.CPUSet
	owner OwnerIdent
}

func (ca claimAllocation) Acquire(owner OwnerIdent) (claimAllocation, bool) {
	if !ca.owner.Vacant() {
		return claimAllocation{}, false
	}
	return claimAllocation{
		cpus:  ca.cpus.Clone(),
		owner: owner,
	}, true
}

func (ca claimAllocation) Release() claimAllocation {
	return claimAllocation{
		cpus: ca.cpus.Clone(),
		// intentional empty owner
	}
}

func (ca claimAllocation) Clone() claimAllocation {
	return claimAllocation{
		cpus: ca.cpus.Clone(),
		owner: OwnerIdent{
			PodUID:        ca.owner.PodUID,
			ContainerName: ca.owner.ContainerName,
		},
	}
}

// CPUAllocation is the single source of truth for CPU allocations.
type CPUAllocation struct {
	mu                       sync.RWMutex
	availableCPUs            cpuset.CPUSet
	reservedCPUs             cpuset.CPUSet
	preparedCPUs             cpuset.CPUSet
	resourceClaimAllocations map[types.UID]claimAllocation
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
		preparedCPUs:             cpuset.New(),
		resourceClaimAllocations: make(map[types.UID]claimAllocation),
	}
}

// ReserveResourceClaimAllocation records a prepared claim. Its CPUs remain unavailable
// to shared containers and other exclusive claims until Unprepare. When shared
// containers are present, the reservation must leave at least one CPU in the
// shared pool because NRI cannot represent an empty CPUSet.
func (s *CPUAllocation) ReserveResourceClaimAllocation(logger logr.Logger, claimUID types.UID, cpus cpuset.CPUSet, hasSharedContainers bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if allocation, ok := s.resourceClaimAllocations[claimUID]; ok {
		if allocation.cpus.Equals(cpus) {
			return nil
		}
		return fmt.Errorf("claim %q is already prepared with CPUs %q (requested %q)", claimUID, allocation.cpus.String(), cpus.String())
	}
	sharedCPUs := s.availableCPUs.Difference(s.preparedCPUs)
	if !cpus.IsSubsetOf(sharedCPUs) {
		return fmt.Errorf("claim %q has overlapping CPU assignment %q", claimUID, cpus.String())
	}
	if hasSharedContainers && !cpus.IsEmpty() && sharedCPUs.Difference(cpus).IsEmpty() {
		return fmt.Errorf("claim %q would exhaust the shared CPU pool while shared containers are running", claimUID)
	}
	s.resourceClaimAllocations[claimUID] = claimAllocation{
		cpus: cpus,
	}
	s.preparedCPUs = s.preparedCPUs.Union(cpus)
	logger.Info("reserved allocation for resource claim", "cpus", cpus.String())
	return nil
}

func (s *CPUAllocation) Cleanup(claimUIDs ...types.UID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, claimUID := range claimUIDs {
		// disown existing allocations.
		// The cpuset partitioning has is bound to the DRA resource claim and must keep the same lifecycle.
		if allocation, ok := s.resourceClaimAllocations[claimUID]; ok {
			s.resourceClaimAllocations[claimUID] = allocation.Release()
		}
	}
}

func (s *CPUAllocation) IsOwner(claimUID types.UID, podUID types.UID, containerName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	info, found := s.resourceClaimAllocations[claimUID]
	if !found {
		return false
	}
	if info.owner.Vacant() {
		return false
	}
	return info.owner.Equal(podUID, containerName)
}

// ValidateResourceClaimAllocations verifies that a container's claims match prepared allocations.
func (s *CPUAllocation) ValidateResourceClaimAllocations(logger logr.Logger, podUID types.UID, containerName string, expected map[types.UID]cpuset.CPUSet) ([]types.UID, error) {
	if len(expected) == 0 {
		return nil, errors.New("no claims to bind")
	}
	curIdent := OwnerIdent{
		PodUID:        podUID,
		ContainerName: containerName,
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for claimUID, cpus := range expected {
		allocation, ok := s.resourceClaimAllocations[claimUID]
		if !ok {
			return nil, fmt.Errorf("claim %q is not prepared by this driver", claimUID)
		}
		if !allocation.cpus.Equals(cpus) {
			return nil, fmt.Errorf("validation failed for claim %q: cpuset mismatch (expected %q, got %q)", claimUID, allocation.cpus.String(), cpus.String())
		}
		if !allocation.owner.Vacant() && !allocation.owner.EqualIdent(curIdent) {
			return nil, AlreadyOwned{
				ClaimUID: claimUID,
				Owner:    allocation.owner,
			}
		}
	}

	newOwners := []types.UID{}
	for claimUID := range expected {
		allocation := s.resourceClaimAllocations[claimUID]
		if allocation, ok := allocation.Acquire(curIdent); ok {
			s.resourceClaimAllocations[claimUID] = allocation
			newOwners = append(newOwners, claimUID)
		}
	}
	return newOwners, nil
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
}

// GetSharedCPUs returns CPUs available to shared containers.
func (s *CPUAllocation) GetSharedCPUs() cpuset.CPUSet {
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

// Clone copies allocation state without sharing maps or locks.
func (s *CPUAllocation) Clone() *CPUAllocation {
	s.mu.RLock()
	defer s.mu.RUnlock()
	allocations := make(map[types.UID]claimAllocation, len(s.resourceClaimAllocations))
	for uid, info := range s.resourceClaimAllocations {
		allocations[uid] = info.Clone()
	}

	return &CPUAllocation{
		availableCPUs:            s.availableCPUs.Clone(),
		reservedCPUs:             s.reservedCPUs.Clone(),
		preparedCPUs:             s.preparedCPUs.Clone(),
		resourceClaimAllocations: allocations,
	}
}
