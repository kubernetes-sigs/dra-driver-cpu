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
	"maps"
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
	Request  string
	Expected cpuset.CPUSet
	Got      cpuset.CPUSet
}

func (err ClaimCPUMismatchError) Error() string {
	// TODO: always format with request name once per-request binding is wired end-to-end.
	if err.Request != "" {
		return fmt.Sprintf("validation failed for claim %q request %q: cpuset mismatch (expected %q, got %q)", err.ClaimUID, err.Request, err.Expected.String(), err.Got.String())
	}
	return fmt.Sprintf("validation failed for claim %q: cpuset mismatch (expected %q, got %q)", err.ClaimUID, err.Expected.String(), err.Got.String())
}

func (err ClaimCPUMismatchError) Unwrap() error {
	return ErrClaimCPUMismatch
}

type UnknownClaimError struct {
	ClaimUID types.UID
	Request  string
}

func (err UnknownClaimError) Error() string {
	// TODO: always format with request name once per-request binding is wired end-to-end.
	if err.Request != "" {
		return fmt.Sprintf("claim %q request %q is not prepared by this driver", err.ClaimUID, err.Request)
	}
	return fmt.Sprintf("claim %q is not prepared by this driver", err.ClaimUID)
}

func (err UnknownClaimError) Unwrap() error {
	return ErrUnknownClaim
}

type OwnerIdent struct {
	PodUID        types.UID
	ContainerName string
}

func (oi OwnerIdent) String() string {
	return string(oi.PodUID) + "/" + oi.ContainerName
}

func (oi OwnerIdent) Assigned() bool {
	return oi.PodUID != types.UID("") && oi.ContainerName != ""
}

func (oi OwnerIdent) Vacant() bool {
	return oi.PodUID == types.UID("") && oi.ContainerName == ""
}

func (oi OwnerIdent) Equal(ident OwnerIdent) bool {
	return oi.PodUID == ident.PodUID && oi.ContainerName == ident.ContainerName
}

type requestAllocation struct {
	cpus   cpuset.CPUSet
	owners []OwnerIdent
}

func (ra requestAllocation) Acquire(owner OwnerIdent) (requestAllocation, bool) {
	if slices.Contains(ra.owners, owner) {
		return ra, false
	}
	owners := make([]OwnerIdent, len(ra.owners), len(ra.owners)+1)
	copy(owners, ra.owners)
	owners = append(owners, owner)
	return requestAllocation{
		cpus:   ra.cpus.Clone(),
		owners: owners,
	}, true
}

func (ra requestAllocation) Release() (requestAllocation, bool) {
	if len(ra.owners) == 0 {
		return ra, false
	}
	return requestAllocation{
		cpus: ra.cpus.Clone(),
	}, true
}

func (ra requestAllocation) Clone() requestAllocation {
	var owners []OwnerIdent
	if len(ra.owners) > 0 {
		owners = make([]OwnerIdent, len(ra.owners))
		copy(owners, ra.owners)
	}
	return requestAllocation{
		cpus:   ra.cpus.Clone(),
		owners: owners,
	}
}

type claimAllocation struct {
	cpus     cpuset.CPUSet
	requests map[string]requestAllocation
}

func newClaimAllocation(claimUID types.UID, requests map[string]cpuset.CPUSet) (claimAllocation, error) {
	if len(requests) == 0 {
		return claimAllocation{}, fmt.Errorf("claim %q has no requests", claimUID)
	}
	totalCPUs := cpuset.New()
	reqAllocs := make(map[string]requestAllocation, len(requests))
	for _, reqName := range slices.Sorted(maps.Keys(requests)) {
		reqCPUs := requests[reqName]
		if reqCPUs.IsEmpty() {
			return claimAllocation{}, fmt.Errorf("claim %q has no CPUs", claimUID)
		}
		if !totalCPUs.Intersection(reqCPUs).IsEmpty() {
			return claimAllocation{}, fmt.Errorf("claim %q has overlapping request CPU assignment %q", claimUID, reqCPUs.String())
		}
		totalCPUs = totalCPUs.Union(reqCPUs)
		reqAllocs[reqName] = requestAllocation{
			cpus: reqCPUs.Clone(),
		}
	}
	return claimAllocation{
		cpus:     totalCPUs,
		requests: reqAllocs,
	}, nil
}

func (ca claimAllocation) SameRequests(requests map[string]cpuset.CPUSet) bool {
	if len(ca.requests) != len(requests) {
		return false
	}
	for reqName, reqCPUs := range requests {
		existing, ok := ca.requests[reqName]
		if !ok || !existing.cpus.Equals(reqCPUs) {
			return false
		}
	}
	return true
}

func (ca claimAllocation) ExpectedCPUs(requestName string) (cpuset.CPUSet, bool) {
	if requestName != "" {
		req, ok := ca.requests[requestName]
		return req.cpus, ok
	}
	return ca.cpus, true
}

func (ca claimAllocation) ConflictOwner(ident OwnerIdent) (OwnerIdent, bool) {
	for _, reqName := range slices.Sorted(maps.Keys(ca.requests)) {
		for _, owner := range ca.requests[reqName].owners {
			if !owner.Equal(ident) {
				return owner, true
			}
		}
	}
	return OwnerIdent{}, false
}

func (ca claimAllocation) HasOwner(requestName string, ident OwnerIdent) bool {
	if requestName != "" {
		req, ok := ca.requests[requestName]
		if !ok {
			return false
		}
		return slices.Contains(req.owners, ident)
	}
	for _, req := range ca.requests {
		if slices.Contains(req.owners, ident) {
			return true
		}
	}
	return false
}

// Acquire records owner on the requested allocation (or all requests when requestName is empty).
func (ca claimAllocation) Acquire(requestName string, owner OwnerIdent) (claimAllocation, bool) {
	if _, conflict := ca.ConflictOwner(owner); conflict {
		return claimAllocation{}, false
	}
	if requestName != "" {
		req, ok := ca.requests[requestName]
		if !ok {
			return claimAllocation{}, false
		}
		updatedReq, acquired := req.Acquire(owner)
		if !acquired {
			return ca, false
		}
		cloned := ca.Clone()
		cloned.requests[requestName] = updatedReq
		return cloned, true
	}
	cloned := ca.Clone()
	acquiredAny := false
	for reqName, req := range cloned.requests {
		updatedReq, acquired := req.Acquire(owner)
		if acquired {
			cloned.requests[reqName] = updatedReq
			acquiredAny = true
		}
	}
	if !acquiredAny {
		return ca, false
	}
	return cloned, true
}

// Release returns a copy without ownership, retaining its CPUs.
func (ca claimAllocation) Release() (claimAllocation, bool) {
	cloned := ca.Clone()
	releasedAny := false
	for reqName, req := range cloned.requests {
		updatedReq, released := req.Release()
		if released {
			cloned.requests[reqName] = updatedReq
			releasedAny = true
		}
	}
	if !releasedAny {
		return ca, false
	}
	return cloned, true
}

func (ca claimAllocation) Clone() claimAllocation {
	requests := make(map[string]requestAllocation, len(ca.requests))
	for reqName, req := range ca.requests {
		requests[reqName] = req.Clone()
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
		resourceClaimAllocations: make(map[types.UID]claimAllocation),
	}
}

// Reserve records a prepared claim. Its CPUs remain unavailable
// to shared containers and other exclusive claims until Unprepare. When shared
// containers are present, the reservation must leave at least one CPU in the
// shared pool because NRI cannot represent an empty CPUSet.
func (s *AllocationState) Reserve(logger logr.Logger, claimUID types.UID, requests map[string]cpuset.CPUSet, hasSharedContainers bool) error {
	logger = logger.WithValues("claimUID", claimUID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if claimUID == "" {
		return fmt.Errorf("invalid claim UID %q", claimUID)
	}
	newAlloc, err := newClaimAllocation(claimUID, requests)
	if err != nil {
		return err
	}
	if allocation, ok := s.resourceClaimAllocations[claimUID]; ok {
		if !allocation.cpus.Equals(newAlloc.cpus) {
			return fmt.Errorf("claim %q is already prepared with CPUs %q (requested %q)", claimUID, allocation.cpus.String(), newAlloc.cpus.String())
		}
		if _, isDefaultOnly := requests[""]; isDefaultOnly && len(requests) == 1 {
			return nil
		}
		if allocation.SameRequests(requests) {
			return nil
		}
		return fmt.Errorf("claim %q is already prepared with different request allocations", claimUID)
	}
	sharedCPUs := s.availableCPUs.Difference(s.preparedCPUs)
	if !newAlloc.cpus.IsSubsetOf(sharedCPUs) {
		return fmt.Errorf("claim %q has overlapping CPU assignment %q", claimUID, newAlloc.cpus.String())
	}
	if hasSharedContainers && sharedCPUs.Difference(newAlloc.cpus).IsEmpty() {
		return fmt.Errorf("claim %q would exhaust the shared CPU pool while shared containers are running", claimUID)
	}
	candidate := s.cloneLocked()
	candidate.resourceClaimAllocations[claimUID] = newAlloc
	candidate.preparedCPUs = candidate.preparedCPUs.Union(newAlloc.cpus)
	s.applyLocked(candidate)
	logger.Info("reserved allocation for resource claim", "cpus", newAlloc.cpus.String())
	return nil
}

// Release removes a prepared claim, releasing its CPUs and ownership.
func (s *AllocationState) Release(logger logr.Logger, claimUID types.UID) {
	logger = logger.WithValues("claimUID", claimUID)
	s.mu.Lock()
	defer s.mu.Unlock()
	allocation, ok := s.resourceClaimAllocations[claimUID]
	if !ok {
		return
	}
	candidate := s.cloneLocked()
	candidate.removeLocked(claimUID)
	s.applyLocked(candidate)
	logger.Info("removed allocation for resource claim", "cpus", allocation.cpus.String())
}

// Bind validates prepared allocations and atomically binds them to a container.
// It returns only newly bound claims so callers can roll back their ownership.
func (s *AllocationState) Bind(logger logr.Logger, podUID types.UID, containerName string, expected map[types.UID]map[string]cpuset.CPUSet) ([]types.UID, error) {
	if len(expected) == 0 {
		return nil, ErrEmptyClaimList
	}
	logger = logger.WithValues("podUID", podUID, "containerName", containerName)
	curIdent := OwnerIdent{
		PodUID:        podUID,
		ContainerName: containerName,
	}
	if !curIdent.Assigned() {
		return nil, fmt.Errorf("invalid claim owner podUID=%q containerName=%q: provide both a pod UID and container name", podUID, containerName)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	for claimUID, requests := range expected {
		if len(requests) == 0 {
			return nil, ErrEmptyClaimList
		}
		allocation, ok := s.resourceClaimAllocations[claimUID]
		if !ok {
			return nil, UnknownClaimError{
				ClaimUID: claimUID,
			}
		}
		for reqName, cpus := range requests {
			expectedCPUs, ok := allocation.ExpectedCPUs(reqName)
			if !ok {
				return nil, UnknownClaimError{
					ClaimUID: claimUID,
					Request:  reqName,
				}
			}
			if !expectedCPUs.Equals(cpus) {
				return nil, ClaimCPUMismatchError{
					ClaimUID: claimUID,
					Request:  reqName,
					Expected: expectedCPUs,
					Got:      cpus,
				}
			}
		}
		if owner, conflict := allocation.ConflictOwner(curIdent); conflict {
			return nil, ClaimAlreadyBoundError{
				ClaimUID: claimUID,
				Owner:    owner,
			}
		}
	}

	candidate := s.cloneLocked()
	updatedClaims := []types.UID{}
	for claimUID, requests := range expected {
		allocation := candidate.resourceClaimAllocations[claimUID]
		acquiredClaim := false
		for reqName := range requests {
			allocInfo, acquired := allocation.Acquire(reqName, curIdent)
			if !acquired {
				continue
			}
			allocation = allocInfo
			acquiredClaim = true
		}
		if !acquiredClaim {
			logger.V(2).Info("claim bound again to the same owner", "claimUID", claimUID)
			continue
		}
		candidate.resourceClaimAllocations[claimUID] = allocation
		updatedClaims = append(updatedClaims, claimUID)
	}
	if len(updatedClaims) == 0 {
		return updatedClaims, nil
	}
	s.applyLocked(candidate)
	for _, claimUID := range updatedClaims {
		logger.V(4).Info("claim bound", "claimUID", claimUID)
	}
	return updatedClaims, nil
}

// Unbind detaches a claim from the container it was bound to, but doesn't release the prepared claim.
func (s *AllocationState) Unbind(logger logr.Logger, claimUIDs ...types.UID) {
	if len(claimUIDs) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	candidate := s.cloneLocked()
	changed := false
	for _, claimUID := range claimUIDs {
		if allocation, ok := candidate.resourceClaimAllocations[claimUID]; ok {
			updated, released := allocation.Release()
			if !released {
				continue
			}
			candidate.resourceClaimAllocations[claimUID] = updated
			changed = true
			logger.V(4).Info("claim unbound", "claimUID", claimUID)
		}
	}
	if !changed {
		return
	}
	s.applyLocked(candidate)
}

// IsOwner checks if a claim, identified by its UID (and optional request names), is owned by the given container.
func (s *AllocationState) IsOwner(claimUID types.UID, podUID types.UID, containerName string, requests ...string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	info, found := s.resourceClaimAllocations[claimUID]
	if !found {
		return false
	}
	ident := OwnerIdent{PodUID: podUID, ContainerName: containerName}
	if !ident.Assigned() {
		return false
	}
	if len(requests) == 0 {
		return info.HasOwner("", ident)
	}
	for _, reqName := range requests {
		if !info.HasOwner(reqName, ident) {
			return false
		}
	}
	return true
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
	return s.cloneLocked()
}

func (s *AllocationState) cloneLocked() *AllocationState {
	allocations := make(map[types.UID]claimAllocation, len(s.resourceClaimAllocations))
	for uid, info := range s.resourceClaimAllocations {
		allocations[uid] = info.Clone()
	}

	return &AllocationState{
		availableCPUs:            s.availableCPUs.Clone(),
		reservedCPUs:             s.reservedCPUs.Clone(),
		preparedCPUs:             s.preparedCPUs.Clone(),
		resourceClaimAllocations: allocations,
	}
}

func (s *AllocationState) applyLocked(candidate *AllocationState) {
	s.resourceClaimAllocations = candidate.resourceClaimAllocations
	s.preparedCPUs = candidate.preparedCPUs
}

func (s *AllocationState) removeLocked(claimUID types.UID) {
	allocation, ok := s.resourceClaimAllocations[claimUID]
	if !ok {
		return
	}
	delete(s.resourceClaimAllocations, claimUID)
	s.preparedCPUs = s.preparedCPUs.Difference(allocation.cpus)
}
