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
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/checkpoint"
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

// Acquire returns an owned copy if the allocation has no owner.
func (ca claimAllocation) Acquire(owner OwnerIdent) (claimAllocation, bool) {
	if !ca.owner.Vacant() {
		return claimAllocation{}, false
	}
	return claimAllocation{
		cpus:  ca.cpus.Clone(),
		owner: owner,
	}, true
}

// Release returns a copy without ownership, retaining its CPUs.
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

// AllocationState is the single source of truth for CPU allocations.
type AllocationState struct {
	mu                       sync.RWMutex
	checkpointMgr            checkpoint.Manager
	dirty                    bool // flag to avoid unnecessary checkpointwrites
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

// NewAllocationState creates a new AllocationState.
func NewAllocationState(cpuTopology *cpuinfo.CPUTopology, reservedCPUs cpuset.CPUSet) *AllocationState {
	return NewCheckpointingAllocationState(checkpoint.NewNOOPManager(), cpuTopology, reservedCPUs)
}

func NewCheckpointingAllocationState(checkpointMgr checkpoint.Manager, cpuTopology *cpuinfo.CPUTopology, reservedCPUs cpuset.CPUSet) *AllocationState {
	cpuIDs := []int{}
	for cpuID := range cpuTopology.CPUDetails {
		cpuIDs = append(cpuIDs, cpuID)
	}
	allCPUsSet := cpuset.New(cpuIDs...)
	availableCPUs := allCPUsSet.Difference(reservedCPUs)

	return &AllocationState{
		checkpointMgr:            checkpointMgr,
		availableCPUs:            availableCPUs,
		reservedCPUs:             reservedCPUs,
		preparedCPUs:             cpuset.New(),
		resourceClaimAllocations: make(map[types.UID]claimAllocation),
	}
}

// Reserve records a prepared claim. Its CPUs remain unavailable
// to shared containers and other exclusive claims until Unprepare. When shared
// containers are present, the reservation must leave at least one CPU in the
// shared pool because NRI cannot represent an empty CPUSet.
func (s *AllocationState) Reserve(logger logr.Logger, claimUID types.UID, cpus cpuset.CPUSet, hasSharedContainers bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.storeStateLocked(logger)

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
	s.dirty = true
	return nil
}

func (s *AllocationState) Unbind(logger logr.Logger, claimUIDs ...types.UID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.storeStateLocked(logger)

	for _, claimUID := range claimUIDs {
		if allocation, ok := s.resourceClaimAllocations[claimUID]; ok {
			s.resourceClaimAllocations[claimUID] = allocation.Release()
		}
	}
	s.dirty = true
}

func (s *AllocationState) IsOwner(claimUID types.UID, podUID types.UID, containerName string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	info, found := s.resourceClaimAllocations[claimUID]
	if !found {
		return false
	}
	if info.owner.Vacant() {
		return false
	}
	return info.owner.Equal(podUID, containerName)
}

// Bind validates prepared allocations and atomically binds them to a container.
// It returns only newly bound claims so callers can roll back their ownership.
func (s *AllocationState) Bind(logger logr.Logger, podUID types.UID, containerName string, expected map[types.UID]cpuset.CPUSet) ([]types.UID, error) {
	if len(expected) == 0 {
		return nil, errors.New("no claims to bind")
	}
	curIdent := OwnerIdent{
		PodUID:        podUID,
		ContainerName: containerName,
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.storeStateLocked(logger)

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
	s.dirty = true
	return newOwners, nil
}

// Release removes a prepared claim, releasing its CPUs and ownership.
func (s *AllocationState) Release(logger logr.Logger, claimUID types.UID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.storeStateLocked(logger)

	if _, ok := s.resourceClaimAllocations[claimUID]; ok {
		s.removeLocked(claimUID)
		logger.Info("removed allocation for resource claim")
	}
	s.dirty = true
}

func (s *AllocationState) removeLocked(claimUID types.UID) {
	allocation, ok := s.resourceClaimAllocations[claimUID]
	if !ok {
		return
	}
	delete(s.resourceClaimAllocations, claimUID)
	s.preparedCPUs = s.preparedCPUs.Difference(allocation.cpus)
}

// GetSharedCPUs returns CPUs available to shared containers.
func (s *AllocationState) GetSharedCPUs() cpuset.CPUSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.availableCPUs.Difference(s.preparedCPUs)
}

// GetResourceClaimAllocation returns the cpuset for a given resource claim.
func (s *AllocationState) GetResourceClaimAllocation(claimUID types.UID) (cpuset.CPUSet, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	allocation, ok := s.resourceClaimAllocations[claimUID]
	return allocation.cpus, ok
}

// GetReservedCPUs returns the set of reserved CPUs.
func (s *AllocationState) GetReservedCPUs() cpuset.CPUSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.reservedCPUs
}

// GetPreparedCPUs returns the CPUs reserved for prepared claims.
func (s *AllocationState) GetPreparedCPUs() cpuset.CPUSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.preparedCPUs
}

// Snapshot returns a point-in-time summary of CPU allocation state.
func (s *AllocationState) Snapshot() AllocationSnapshot {
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
func (s *AllocationState) Clone() *AllocationState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	allocations := make(map[types.UID]claimAllocation, len(s.resourceClaimAllocations))
	for uid, info := range s.resourceClaimAllocations {
		allocations[uid] = info.Clone()
	}

	return &AllocationState{
		checkpointMgr:            s.checkpointMgr,
		availableCPUs:            s.availableCPUs.Clone(),
		reservedCPUs:             s.reservedCPUs.Clone(),
		preparedCPUs:             s.preparedCPUs.Clone(),
		resourceClaimAllocations: allocations,
	}
}
