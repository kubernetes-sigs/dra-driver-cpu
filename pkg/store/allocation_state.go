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

package store

import (
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/cpuset"
)

var (
	ErrClaimAlreadyBound = errors.New("claim is already bound to another container")
	ErrClaimCPUMismatch  = errors.New("claim cpuset does not match the prepared allocation")
	ErrUnknownClaim      = errors.New("claim is not prepared by this driver")
	ErrEmptyClaimList    = errors.New("claim list is empty")
)

type ClaimAlreadyBoundError struct {
	ClaimUID types.UID
	Owner    OwnerIdent
}

func (err ClaimAlreadyBoundError) Error() string {
	return fmt.Sprintf("claimUID %q is already bound to pod %q container %q", err.ClaimUID, err.Owner.PodUID, err.Owner.ContainerName)
}

func (err ClaimAlreadyBoundError) Unwrap() error {
	return ErrClaimAlreadyBound
}

type ClaimCPUMismatchError struct {
	ClaimUID types.UID
	Expected cpuset.CPUSet
	Got      cpuset.CPUSet
}

func (err ClaimCPUMismatchError) Error() string {
	return fmt.Sprintf("validation failed for claim %q: cpuset mismatch (expected %q, got %q)", err.ClaimUID, err.Expected.String(), err.Got.String())
}

func (err ClaimCPUMismatchError) Unwrap() error {
	return ErrClaimCPUMismatch
}

type UnknownClaimError struct {
	ClaimUID types.UID
}

func (err UnknownClaimError) Error() string {
	return fmt.Sprintf("claim %q is not prepared by this driver", err.ClaimUID)
}

func (err UnknownClaimError) Unwrap() error {
	return ErrUnknownClaim
}

type OwnerIdent struct {
	PodUID        types.UID
	ContainerName string
}

func (oi OwnerIdent) Assigned() bool {
	return oi.PodUID != types.UID("") && oi.ContainerName != ""
}

func (oi OwnerIdent) Equal(ident OwnerIdent) bool {
	return oi.PodUID == ident.PodUID && oi.ContainerName == ident.ContainerName
}

type requestAllocation struct {
	cpus   cpuset.CPUSet
	owners []OwnerIdent
}

// claimAllocation keeps CPUs and owners per request, so a container can later
// bind only the requests it uses. An empty request name ("") represents the
// entire claim when per-request names are not available, for example when
// restoring from an env var in NRI.
// TODO: bind and validate CPUs per request once CDI is split per request.
// See: https://github.com/kubernetes-sigs/dra-driver-cpu/issues/389
type claimAllocation struct {
	cpus     cpuset.CPUSet // union of all requests
	requests map[string]requestAllocation
}

func newClaimAllocation(claimUID types.UID, requests map[string]cpuset.CPUSet) (claimAllocation, error) {
	if len(requests) == 0 {
		return claimAllocation{}, fmt.Errorf("claim %q has no CPUs", claimUID)
	}
	allocation := claimAllocation{
		cpus:     cpuset.New(),
		requests: make(map[string]requestAllocation, len(requests)),
	}
	for name, cpus := range requests {
		if cpus.IsEmpty() {
			return claimAllocation{}, fmt.Errorf("claim %q request %q has no CPUs", claimUID, name)
		}
		if !allocation.cpus.Intersection(cpus).IsEmpty() {
			return claimAllocation{}, fmt.Errorf("claim %q request %q has CPUs %q that overlap another request", claimUID, name, cpus.String())
		}
		allocation.cpus = allocation.cpus.Union(cpus)
		allocation.requests[name] = requestAllocation{cpus: cpus}
	}
	return allocation, nil
}

// Acquire returns a copy owned by owner, and whether owner is new. The CDI env
// has one cpuset per claim, so a container binds every request of the claim.
func (ca claimAllocation) Acquire(owner OwnerIdent) (claimAllocation, bool) {
	allocation := ca.Clone()
	acquired := false
	for name, req := range allocation.requests {
		if slices.Contains(req.owners, owner) {
			continue
		}
		req.owners = append(req.owners, owner)
		allocation.requests[name] = req
		acquired = true
	}
	return allocation, acquired
}

// Release returns a copy without owners, retaining its CPUs.
func (ca claimAllocation) Release() claimAllocation {
	allocation := ca.Clone()
	for name, req := range allocation.requests {
		// intentional empty owner
		req.owners = nil
		allocation.requests[name] = req
	}
	return allocation
}

// AllClaimOwners returns the containers bound to any request of the claim.
func (ca claimAllocation) AllClaimOwners() []OwnerIdent {
	var owners []OwnerIdent
	for _, req := range ca.requests {
		for _, owner := range req.owners {
			if !slices.Contains(owners, owner) {
				owners = append(owners, owner)
			}
		}
	}
	return owners
}

func (ca claimAllocation) Clone() claimAllocation {
	requests := make(map[string]requestAllocation, len(ca.requests))
	for name, req := range ca.requests {
		requests[name] = requestAllocation{
			cpus:   req.cpus.Clone(),
			owners: slices.Clone(req.owners),
		}
	}
	return claimAllocation{
		cpus:     ca.cpus.Clone(),
		requests: requests,
	}
}

// AllocationState is the single source of truth for CPU allocations.
type AllocationState struct {
	mu                       sync.RWMutex
	availableCPUs            cpuset.CPUSet
	reservedCPUs             cpuset.CPUSet
	preparedCPUs             cpuset.CPUSet
	shareReservedCPUs        bool
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
func NewAllocationState(cpuTopology *cpuinfo.CPUTopology, reservedCPUs cpuset.CPUSet, reservedCPUsPolicy string) *AllocationState {
	cpuIDs := []int{}
	for cpuID := range cpuTopology.CPUDetails {
		cpuIDs = append(cpuIDs, cpuID)
	}
	allCPUsSet := cpuset.New(cpuIDs...)
	availableCPUs := allCPUsSet.Difference(reservedCPUs)

	return &AllocationState{
		availableCPUs:            availableCPUs,
		reservedCPUs:             reservedCPUs,
		preparedCPUs:             cpuset.New(),
		shareReservedCPUs:        reservedCPUsPolicy == cpuinfo.ReservedCPUsPolicyShared,
		resourceClaimAllocations: make(map[types.UID]claimAllocation),
	}
}

// Reserve records a prepared claim. Its CPUs remain unavailable
// to shared containers and other exclusive claims until Unprepare. When shared
// containers are present, the reservation must leave at least one CPU in the
// shared pool because NRI cannot represent an empty CPUSet.
func (s *AllocationState) Reserve(logger logr.Logger, claimUID types.UID, requests map[string]cpuset.CPUSet, hasSharedContainers bool) error {
	logger = logger.WithValues("claimUID", claimUID)
	newAllocation, err := newClaimAllocation(claimUID, requests)
	if err != nil {
		return err
	}
	cpus := newAllocation.cpus
	s.mu.Lock()
	defer s.mu.Unlock()
	if allocation, ok := s.resourceClaimAllocations[claimUID]; ok {
		if allocation.cpus.Equals(cpus) {
			return nil
		}
		return fmt.Errorf("claim %q is already prepared with CPUs %q (requested %q)", claimUID, allocation.cpus.String(), cpus.String())
	}
	if !cpus.IsSubsetOf(s.availableLocked()) {
		return fmt.Errorf("claim %q has overlapping CPU assignment %q", claimUID, cpus.String())
	}
	if hasSharedContainers && !cpus.IsEmpty() && s.sharedLocked().Difference(cpus).IsEmpty() {
		return fmt.Errorf("claim %q would exhaust the shared CPU pool while shared containers are running", claimUID)
	}
	s.resourceClaimAllocations[claimUID] = newAllocation
	s.preparedCPUs = s.preparedCPUs.Union(cpus)
	logger.Info("reserved allocation for resource claim", "cpus", cpus.String())
	return nil
}

// Release removes a prepared claim, releasing its CPUs and ownership.
func (s *AllocationState) Release(logger logr.Logger, claimUID types.UID) {
	logger = logger.WithValues("claimUID", claimUID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if allocInfo, ok := s.resourceClaimAllocations[claimUID]; ok {
		s.removeLocked(claimUID)
		logger.Info("removed allocation for resource claim", "cpus", allocInfo.cpus.String())
	}
}

// Bind validates prepared allocations and atomically binds them to a container.
// It returns only newly bound claims so callers can roll back their ownership.
func (s *AllocationState) Bind(logger logr.Logger, podUID types.UID, containerName string, expected map[types.UID]cpuset.CPUSet) ([]types.UID, error) {
	if len(expected) == 0 {
		return nil, ErrEmptyClaimList
	}
	logger = logger.WithValues("podUID", podUID, "containerName", containerName)
	curIdent := OwnerIdent{
		PodUID:        podUID,
		ContainerName: containerName,
	}
	// An empty owner would be stored as a real entry in the owner list.
	if !curIdent.Assigned() {
		return nil, fmt.Errorf("invalid claim owner podUID=%q containerName=%q: provide both a pod UID and container name", podUID, containerName)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for claimUID, cpus := range expected {
		allocation, ok := s.resourceClaimAllocations[claimUID]
		if !ok {
			return nil, UnknownClaimError{
				ClaimUID: claimUID,
			}
		}
		if !allocation.cpus.Equals(cpus) {
			return nil, ClaimCPUMismatchError{
				ClaimUID: claimUID,
				Expected: allocation.cpus,
				Got:      cpus,
			}
		}
		// Claims are not shareable yet, so any other owner is a conflict.
		for _, owner := range allocation.AllClaimOwners() {
			if !owner.Equal(curIdent) {
				return nil, ClaimAlreadyBoundError{
					ClaimUID: claimUID,
					Owner:    owner,
				}
			}
		}
	}

	updatedClaims := []types.UID{}
	for claimUID := range expected {
		allocation := s.resourceClaimAllocations[claimUID]
		allocInfo, acquired := allocation.Acquire(curIdent)
		if !acquired {
			logger.V(2).Info("claim bound again to the same owner", "claimUID", claimUID)
			continue
		}
		s.resourceClaimAllocations[claimUID] = allocInfo
		updatedClaims = append(updatedClaims, claimUID)
		logger.V(4).Info("claim bound", "claimUID", claimUID)
	}
	return updatedClaims, nil
}

// Unbind detaches a claim from the container it was bound to, but doesn't release the prepared claim.
func (s *AllocationState) Unbind(logger logr.Logger, claimUIDs ...types.UID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, claimUID := range claimUIDs {
		if allocation, ok := s.resourceClaimAllocations[claimUID]; ok {
			previousOwners := allocation.AllClaimOwners()
			s.resourceClaimAllocations[claimUID] = allocation.Release()
			logger.V(4).Info("claim unbound", "claimUID", claimUID, "previousOwners", previousOwners)
		}
	}
}

// IsOwner checks if a claim, identified by its UID, is owned by the given container
func (s *AllocationState) IsOwner(claimUID types.UID, podUID types.UID, containerName string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	info, found := s.resourceClaimAllocations[claimUID]
	if !found {
		return false
	}
	return slices.Contains(info.AllClaimOwners(), OwnerIdent{PodUID: podUID, ContainerName: containerName})
}

// GetAvailableCPUs returns the CPUs exclusive claims can still be prepared on.
func (s *AllocationState) GetAvailableCPUs() cpuset.CPUSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.availableLocked()
}

// GetSharedCPUs returns CPUs available to shared containers.
func (s *AllocationState) GetSharedCPUs() cpuset.CPUSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sharedLocked()
}

func (s *AllocationState) availableLocked() cpuset.CPUSet {
	return s.availableCPUs.Difference(s.preparedCPUs)
}

func (s *AllocationState) sharedLocked() cpuset.CPUSet {
	shared := s.availableLocked()
	if s.shareReservedCPUs {
		shared = shared.Union(s.reservedCPUs)
	}
	return shared
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
		AvailableCPUs:        s.availableLocked().Size(),
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
		availableCPUs:            s.availableCPUs.Clone(),
		reservedCPUs:             s.reservedCPUs.Clone(),
		preparedCPUs:             s.preparedCPUs.Clone(),
		shareReservedCPUs:        s.shareReservedCPUs,
		resourceClaimAllocations: allocations,
	}
}

func (s *AllocationState) removeLocked(claimUID types.UID) {
	allocation, ok := s.resourceClaimAllocations[claimUID]
	if !ok {
		return
	}
	delete(s.resourceClaimAllocations, claimUID)
	s.preparedCPUs = s.preparedCPUs.Difference(allocation.cpus)
}
