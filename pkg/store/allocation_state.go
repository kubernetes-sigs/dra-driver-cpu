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
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/checkpoint"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	resourceapi "k8s.io/api/resource/v1"
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
	Owner    OwnerIdentifier
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

type OwnerIdentifier struct {
	PodUID        types.UID
	ContainerName string
}

func (oi OwnerIdentifier) String() string {
	return string(oi.PodUID) + "/" + oi.ContainerName
}

func (oi OwnerIdentifier) Assigned() bool {
	return oi.PodUID != types.UID("") && oi.ContainerName != ""
}

func (oi OwnerIdentifier) Vacant() bool {
	return oi.PodUID == types.UID("") && oi.ContainerName == ""
}

func (oi OwnerIdentifier) Equal(ident OwnerIdentifier) bool {
	return oi.PodUID == ident.PodUID && oi.ContainerName == ident.ContainerName
}

type ClaimIdentifier struct {
	UID   types.UID
	Claim types.NamespacedName
}

func ClaimIdentifierFrom(claim *resourceapi.ResourceClaim) ClaimIdentifier {
	return ClaimIdentifier{
		UID: claim.UID,
		Claim: types.NamespacedName{
			Namespace: claim.Namespace,
			Name:      claim.Name,
		},
	}
}

func (ci ClaimIdentifier) Valid() bool {
	return ci.UID != ""
}

func (ci ClaimIdentifier) String() string {
	return fmt.Sprintf("<UID=%q %q>", ci.UID, ci.Claim.String())
}

type requestAllocation struct {
	cpus   cpuset.CPUSet
	owners []OwnerIdentifier
}

// claimAllocation keeps CPUs and owners per request, so a container can later
// bind only the requests it uses. An empty request name ("") represents the
// entire claim when per-request names are not available, for example when
// restoring from an env var in NRI.
// TODO: bind and validate CPUs per request once CDI is split per request.
// See: https://github.com/kubernetes-sigs/dra-driver-cpu/issues/389
type claimAllocation struct {
	claim    types.NamespacedName
	cpus     cpuset.CPUSet // union of all requests
	requests map[string]requestAllocation
}

func newClaimAllocation(claim ClaimIdentifier, requests map[string]cpuset.CPUSet) (claimAllocation, error) {
	if claim.UID == "" {
		return claimAllocation{}, errors.New("missing claim UID")
	}
	if len(requests) == 0 {
		return claimAllocation{}, fmt.Errorf("claim %q has no CPUs", claim)
	}
	allocation := claimAllocation{
		claim:    claim.Claim,
		cpus:     cpuset.New(),
		requests: make(map[string]requestAllocation, len(requests)),
	}
	for name, cpus := range requests {
		if cpus.IsEmpty() {
			return claimAllocation{}, fmt.Errorf("claim %q request %q has no CPUs", claim, name)
		}
		if !allocation.cpus.Intersection(cpus).IsEmpty() {
			return claimAllocation{}, fmt.Errorf("claim %q request %q has CPUs %q that overlap another request", claim, name, cpus.String())
		}
		allocation.cpus = allocation.cpus.Union(cpus)
		allocation.requests[name] = requestAllocation{cpus: cpus}
	}
	return allocation, nil
}

// Acquire returns an owned copy if the allocation has no owner.
func (ca claimAllocation) Acquire(owner OwnerIdentifier) (claimAllocation, bool) {
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
func (ca claimAllocation) AllClaimOwners() []OwnerIdentifier {
	var owners []OwnerIdentifier
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
		claim:    ca.claim,
		cpus:     ca.cpus.Clone(),
		requests: requests,
	}
}

// AllocationState is the single source of truth for CPU allocations.
type AllocationState struct {
	mu                       sync.RWMutex
	nodeName                 string
	availableCPUs            cpuset.CPUSet
	reservedCPUs             cpuset.CPUSet
	preparedCPUs             cpuset.CPUSet
	resourceClaimAllocations map[types.UID]claimAllocation
	topologyFingerprint      string
}

// AllocationSnapshot is a point-in-time summary of CPU allocation state.
type AllocationSnapshot struct {
	AllocatedCPUs        int
	AvailableCPUs        int
	ReservedCPUs         int
	ActiveResourceClaims int
}

// NewAllocationState creates a new AllocationState bound to a node.
func NewAllocationState(nodeName string, cpuTopology *cpuinfo.CPUTopology, reservedCPUs cpuset.CPUSet) *AllocationState {
	cpuIDs := []int{}
	for cpuID := range cpuTopology.CPUDetails {
		cpuIDs = append(cpuIDs, cpuID)
	}
	allCPUsSet := cpuset.New(cpuIDs...)
	availableCPUs := allCPUsSet.Difference(reservedCPUs)

	return &AllocationState{
		nodeName:                 nodeName,
		availableCPUs:            availableCPUs,
		reservedCPUs:             reservedCPUs,
		preparedCPUs:             cpuset.New(),
		resourceClaimAllocations: make(map[types.UID]claimAllocation),
		topologyFingerprint:      MakeTopologyFingerprint(*cpuTopology),
	}
}

// Reserve records a prepared claim. Its CPUs remain unavailable
// to shared containers and other exclusive claims until Unprepare. When shared
// containers are present, the reservation must leave at least one CPU in the
// shared pool because NRI cannot represent an empty CPUSet.
func (s *AllocationState) Reserve(logger logr.Logger, checkpointMgr checkpoint.Manager, claim ClaimIdentifier, requests map[string]cpuset.CPUSet, hasSharedContainers bool) error {
	newAllocation, err := newClaimAllocation(claim, requests)
	if err != nil {
		return err
	}
	cpus := newAllocation.cpus // shortcut
	s.mu.Lock()
	defer s.mu.Unlock()
	if !claim.Valid() {
		return fmt.Errorf("invalid claim identifier %+v", claim)
	}
	if cpus.IsEmpty() {
		return fmt.Errorf("claim %q has no CPUs", claim.UID)
	}
	if allocation, ok := s.resourceClaimAllocations[claim.UID]; ok {
		if allocation.cpus.Equals(cpus) {
			return nil
		}
		return fmt.Errorf("claim %q is already prepared with CPUs %q (requested %q)", claim.UID, allocation.cpus.String(), cpus.String())
	}
	sharedCPUs := s.availableCPUs.Difference(s.preparedCPUs)
	if !cpus.IsSubsetOf(sharedCPUs) {
		return fmt.Errorf("claim %q has overlapping CPU assignment %q", claim.UID, cpus.String())
	}
	if hasSharedContainers && sharedCPUs.Difference(cpus).IsEmpty() {
		return fmt.Errorf("claim %q would exhaust the shared CPU pool while shared containers are running", claim.UID)
	}
	candidate := s.cloneLocked()
	candidate.resourceClaimAllocations[claim.UID] = newAllocation
	candidate.preparedCPUs = candidate.preparedCPUs.Union(cpus)
	if err := s.commitLocked(logger, checkpointMgr, candidate); err != nil {
		return err
	}
	logger.Info("reserved allocation for resource claim", "cpus", cpus.String())
	return nil
}

// Release removes a prepared claim, releasing its CPUs and ownership.
func (s *AllocationState) Release(logger logr.Logger, checkpointMgr checkpoint.Manager, claimUID types.UID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	allocation, ok := s.resourceClaimAllocations[claimUID]
	if !ok {
		return nil
	}
	candidate := s.cloneLocked()
	candidate.removeLocked(claimUID)
	if err := s.commitLocked(logger, checkpointMgr, candidate); err != nil {
		return err
	}
	logger.Info("removed allocation for resource claim", "cpus", allocation.cpus.String())
	return nil
}

// Bind validates prepared allocations and atomically binds them to a container.
// It returns only newly bound claims so callers can roll back their ownership.
func (s *AllocationState) Bind(logger logr.Logger, checkpointMgr checkpoint.Manager, podUID types.UID, containerName string, expected map[types.UID]cpuset.CPUSet) ([]types.UID, error) {
	if len(expected) == 0 {
		return nil, ErrEmptyClaimList
	}
	logger = logger.WithValues("podUID", podUID, "containerName", containerName)
	curIdent := OwnerIdentifier{
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

	candidate := s.cloneLocked()
	updatedClaims := []types.UID{}
	for claimUID := range expected {
		allocation := candidate.resourceClaimAllocations[claimUID]
		allocInfo, acquired := allocation.Acquire(curIdent)
		if !acquired {
			logger.V(2).Info("claim bound again to the same owner", "claimUID", claimUID)
			continue
		}
		candidate.resourceClaimAllocations[claimUID] = allocInfo
		updatedClaims = append(updatedClaims, claimUID)
	}
	if len(updatedClaims) == 0 {
		return updatedClaims, nil
	}
	if err := s.commitLocked(logger, checkpointMgr, candidate); err != nil {
		return nil, err
	}
	for _, claimUID := range updatedClaims {
		logger.V(4).Info("claim bound", "claimUID", claimUID)
	}
	return updatedClaims, nil
}

// Unbind detaches a claim from the container it was bound to, but doesn't release the prepared claim.
func (s *AllocationState) Unbind(logger logr.Logger, checkpointMgr checkpoint.Manager, claimUIDs ...types.UID) error {
	if len(claimUIDs) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	candidate := s.cloneLocked()
	previousOwners := make(map[types.UID]OwnerIdentifier)
	changed := false
	for _, claimUID := range claimUIDs {
		if allocation, ok := candidate.resourceClaimAllocations[claimUID]; ok {
			previousOwners := allocation.AllClaimOwners()
			if len(previousOwners) == 0 {
				continue
			}
			candidate.resourceClaimAllocations[claimUID] = allocation.Release()
			logger.V(4).Info("claim unbound", "claimUID", claimUID, "previousOwners", previousOwners)
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if err := s.commitLocked(logger, checkpointMgr, candidate); err != nil {
		return err
	}
	for claimUID, oldOwner := range previousOwners {
		logger.V(4).Info("claim unbound", "claimUID", claimUID, "previousPodUID", oldOwner.PodUID, "previousContainerName", oldOwner.ContainerName)
	}
	return nil
}

// IsOwner checks if a claim, identified by its UID, is owned by the given container
func (s *AllocationState) IsOwner(claimUID types.UID, podUID types.UID, containerName string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	info, found := s.resourceClaimAllocations[claimUID]
	if !found {
		return false
	}
	return slices.Contains(info.AllClaimOwners(), OwnerIdentifier{PodUID: podUID, ContainerName: containerName})
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
		nodeName:                 s.nodeName,
		availableCPUs:            s.availableCPUs.Clone(),
		reservedCPUs:             s.reservedCPUs.Clone(),
		preparedCPUs:             s.preparedCPUs.Clone(),
		resourceClaimAllocations: allocations,
		topologyFingerprint:      s.topologyFingerprint,
	}
}

func (s *AllocationState) commitLocked(logger logr.Logger, checkpointMgr checkpoint.Manager, candidate *AllocationState) error {
	if err := candidate.storeStateLocked(logger, checkpointMgr); err != nil {
		return err
	}
	s.applyLocked(candidate)
	return nil
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
