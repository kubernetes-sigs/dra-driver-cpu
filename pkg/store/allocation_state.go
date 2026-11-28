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
	"iter"
	"maps"
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
	ErrClaimAlreadyBound = errors.New("claim is already bound")
	ErrClaimCPUMismatch  = errors.New("claim cpuset does not match the prepared allocation")
	ErrUnknownClaim      = errors.New("claim is not prepared by this driver")
	ErrEmptyClaimList    = errors.New("claim list is empty")
)

// ClaimAlreadyBoundError reports that a claim is already bound to a pod.
type ClaimAlreadyBoundError struct {
	ClaimUID types.UID
	PodUID   types.UID
}

func (err ClaimAlreadyBoundError) Error() string {
	return fmt.Sprintf("claimUID %q is already bound to pod %q", err.ClaimUID, err.PodUID)
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

// ClaimRequest identifies a single request within a ResourceClaim.
// An empty Request targets all requests of the claim - used when restoring
// existing containers created before per-request tracking.
type ClaimRequest struct {
	ClaimUID types.UID
	Request  string
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

// requestAllocation holds the CPUs allocated to one request in a claim and the
// container names within the claim's owning pod bound to that request.
type requestAllocation struct {
	cpus       cpuset.CPUSet
	containers []string
}

// claimAllocation holds a prepared ResourceClaim's CPUs, the single pod that
// owns it once bound, and its per-request allocations. ownerPod is non-empty
// if and only if at least one request has a bound container. requests is keyed
// by ResourceClaim request name, or "" when restoring existing containers
// created before per-request tracking
// (https://github.com/kubernetes-sigs/dra-driver-cpu/issues/389).
type claimAllocation struct {
	claim    types.NamespacedName
	ownerPod types.UID
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
	allocation := claimAllocation{claim: claim.Claim}
	requestsSeq := func(yield func(string, requestAllocation) bool) {
		for name, cpus := range requests {
			if !yield(name, requestAllocation{cpus: cpus}) {
				return
			}
		}
	}
	if err := fillClaimAllocationFromRequests(&allocation, requestsSeq, len(requests)); err != nil {
		return claimAllocation{}, fmt.Errorf("claim %q: %w", claim, err)
	}
	return allocation, nil
}

func fillClaimAllocationFromRequests(ca *claimAllocation, requests iter.Seq2[string, requestAllocation], requestCount int) error {
	ca.cpus = cpuset.New()
	ca.requests = make(map[string]requestAllocation, requestCount)
	for name, req := range requests {
		if req.cpus.IsEmpty() {
			return fmt.Errorf("request %q has no CPUs", name)
		}
		if !ca.cpus.Intersection(req.cpus).IsEmpty() {
			return fmt.Errorf("request %q has CPUs %q that overlap another request", name, req.cpus.String())
		}
		ca.cpus = ca.cpus.Union(req.cpus)
		ca.requests[name] = req
	}
	if len(ca.requests) == 0 {
		return errors.New("allocation has no requests")
	}
	if _, hasLegacy := ca.requests[""]; hasLegacy && len(ca.requests) > 1 {
		return errors.New("cannot mix whole-claim and named requests")
	}
	return nil
}

// bind validates expectedCPUs and ownership for claimReq (all requests when
// claimReq.Request is empty), then attaches containerName in podUID. It returns
// the request names actually mutated. Callers must operate on a cloned AllocationState.
func (ca *claimAllocation) bind(claimReq ClaimRequest, podUID types.UID, containerName string, expectedCPUs cpuset.CPUSet) ([]string, error) {
	var targetCPUs cpuset.CPUSet
	var existingContainers []string
	if claimReq.Request == "" {
		targetCPUs = ca.cpus
		existingContainers = ca.boundContainers()
	} else {
		req, ok := ca.requests[claimReq.Request]
		if !ok {
			return nil, UnknownClaimError{ClaimUID: claimReq.ClaimUID}
		}
		targetCPUs = req.cpus
		existingContainers = req.containers
	}
	if !targetCPUs.Equals(expectedCPUs) {
		return nil, ClaimCPUMismatchError{
			ClaimUID: claimReq.ClaimUID,
			Expected: targetCPUs,
			Got:      expectedCPUs,
		}
	}
	// A claim is owned by at most one pod; cross-pod sharing is not allowed
	// even across different requests of the same claim.
	if ca.ownerPod != "" && ca.ownerPod != podUID {
		return nil, ClaimAlreadyBoundError{ClaimUID: claimReq.ClaimUID, PodUID: ca.ownerPod}
	}
	// Requests are not shareable within a pod yet.
	for _, c := range existingContainers {
		if c != containerName {
			return nil, ClaimAlreadyBoundError{ClaimUID: claimReq.ClaimUID, PodUID: ca.ownerPod}
		}
	}

	var changed []string
	for name, req := range ca.requests {
		if claimReq.Request != "" && name != claimReq.Request {
			continue
		}
		if slices.Contains(req.containers, containerName) {
			continue
		}
		req.containers = append(req.containers, containerName)
		ca.requests[name] = req
		changed = append(changed, name)
	}
	if len(changed) > 0 {
		ca.ownerPod = podUID
		slices.Sort(changed)
	}
	return changed, nil
}

// unbind removes containerName from request (or all requests when request is
// empty), retaining its CPUs and other bound containers, and reports whether
// any request changed. If no bound containers remain on any request, ownerPod
// is cleared. Callers must operate on a cloned AllocationState.
func (ca *claimAllocation) unbind(request string, podUID types.UID, containerName string) bool {
	if ca.ownerPod != podUID {
		return false
	}
	removed := false
	for name, req := range ca.requests {
		if request != "" && name != request {
			continue
		}
		before := len(req.containers)
		req.containers = slices.DeleteFunc(req.containers, func(c string) bool {
			return c == containerName
		})
		if len(req.containers) == before {
			continue
		}
		if len(req.containers) == 0 {
			req.containers = nil
		}
		ca.requests[name] = req
		removed = true
	}
	if removed && len(ca.boundContainers()) == 0 {
		ca.ownerPod = ""
	}
	return removed
}

// boundContainers returns the unique container names bound to any request of the claim.
func (ca claimAllocation) boundContainers() []string {
	var containers []string
	for _, req := range ca.requests {
		for _, c := range req.containers {
			if !slices.Contains(containers, c) {
				containers = append(containers, c)
			}
		}
	}
	return containers
}

func (ca claimAllocation) clone() claimAllocation {
	requests := make(map[string]requestAllocation, len(ca.requests))
	for name, req := range ca.requests {
		requests[name] = requestAllocation{
			cpus:       req.cpus.Clone(),
			containers: slices.Clone(req.containers),
		}
	}
	return claimAllocation{
		claim:    ca.claim,
		ownerPod: ca.ownerPod,
		cpus:     ca.cpus.Clone(),
		requests: requests,
	}
}

// AllocationState is the single source of truth for CPU allocations on a node.
// It tracks two lifecycles:
//   - DRA (claim-level): Reserve and Release add and remove prepared claim CPUs.
//   - NRI (container-level): Bind, Unbind, and IsOwner attach containers within
//     a claim's owning pod to individual requests of that claim.
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
		if !allocation.cpus.Equals(cpus) {
			return fmt.Errorf("claim %q is already prepared with CPUs %q (requested %q)", claim.UID, allocation.cpus.String(), cpus.String())
		}
		// Equal union CPUs can still have different per-request splits.
		if !maps.EqualFunc(allocation.requests, requests, func(existing requestAllocation, want cpuset.CPUSet) bool {
			return existing.cpus.Equals(want)
		}) {
			return fmt.Errorf("claim %q is already prepared with different per-request CPU allocations", claim.UID)
		}
		return nil
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

// Bind validates prepared allocations and atomically binds containerName in
// podUID to every entry in expected. Each claim can be owned by at most one
// pod, and each request can be bound to at most one container within that pod.
// It returns only newly bound entries so callers can roll back on failure.
func (s *AllocationState) Bind(logger logr.Logger, checkpointMgr checkpoint.Manager, podUID types.UID, containerName string, expected map[ClaimRequest]cpuset.CPUSet) ([]ClaimRequest, error) {
	if len(expected) == 0 {
		return nil, ErrEmptyClaimList
	}
	if podUID == "" || containerName == "" {
		return nil, fmt.Errorf("invalid container identity podUID=%q containerName=%q: provide both a pod UID and container name", podUID, containerName)
	}
	logger = logger.WithValues("podUID", podUID, "containerName", containerName)

	s.mu.Lock()
	defer s.mu.Unlock()

	candidate := s.cloneLocked()
	var updated []ClaimRequest
	for claimReq, cpus := range expected {
		allocation, ok := candidate.resourceClaimAllocations[claimReq.ClaimUID]
		if !ok {
			return nil, UnknownClaimError{ClaimUID: claimReq.ClaimUID}
		}
		changedRequests, err := allocation.bind(claimReq, podUID, containerName, cpus)
		if err != nil {
			return nil, err
		}
		if len(changedRequests) == 0 {
			logger.V(2).Info("claim bound again to the same container", "claimUID", claimReq.ClaimUID, "request", claimReq.Request)
			continue
		}
		candidate.resourceClaimAllocations[claimReq.ClaimUID] = allocation
		for _, name := range changedRequests {
			updated = append(updated, ClaimRequest{ClaimUID: claimReq.ClaimUID, Request: name})
		}
	}
	if len(updated) == 0 {
		return nil, nil
	}
	if err := s.commitLocked(logger, checkpointMgr, candidate); err != nil {
		return nil, err
	}
	for _, claimReq := range updated {
		logger.V(4).Info("claim bound", "claimUID", claimReq.ClaimUID, "request", claimReq.Request)
	}
	return updated, nil
}

// Unbind detaches containerName in podUID from the given claim requests without
// releasing the prepared claims. If no bound containers remain on a claim, its
// owning pod is cleared.
func (s *AllocationState) Unbind(logger logr.Logger, checkpointMgr checkpoint.Manager, podUID types.UID, containerName string, requests ...ClaimRequest) error {
	if len(requests) == 0 || podUID == "" || containerName == "" {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	candidate := s.cloneLocked()
	changed := 0
	for _, claimReq := range requests {
		allocation, ok := candidate.resourceClaimAllocations[claimReq.ClaimUID]
		if !ok {
			logger.V(2).Info("claim was not reserved, ignored", "claimUID", claimReq.ClaimUID)
			continue
		}
		if !allocation.unbind(claimReq.Request, podUID, containerName) {
			continue
		}
		candidate.resourceClaimAllocations[claimReq.ClaimUID] = allocation
		logger.V(4).Info("unbinding claim", "claimUID", claimReq.ClaimUID, "request", claimReq.Request, "podUID", podUID, "containerName", containerName)
		changed++
	}
	if changed == 0 {
		return nil
	}
	if err := s.commitLocked(logger, checkpointMgr, candidate); err != nil {
		return err
	}
	logger.V(4).Info("claim unbind completed", "requests", requests, "unbound", changed, "requested", len(requests))
	return nil
}

// IsOwner reports whether containerName in podUID is bound to req.
// When req.Request is empty, containerName must be bound to every request
// of the claim.
func (s *AllocationState) IsOwner(podUID types.UID, containerName string, req ClaimRequest) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	info, found := s.resourceClaimAllocations[req.ClaimUID]
	if !found || info.ownerPod == "" || info.ownerPod != podUID {
		return false
	}
	if req.Request != "" {
		r, ok := info.requests[req.Request]
		return ok && slices.Contains(r.containers, containerName)
	}
	for _, r := range info.requests {
		if !slices.Contains(r.containers, containerName) {
			return false
		}
	}
	return len(info.requests) > 0
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

// GetResourceClaimRequests returns a copy of the per-request CPU allocations for a claim.
func (s *AllocationState) GetResourceClaimRequests(claimUID types.UID) (map[string]cpuset.CPUSet, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	allocation, ok := s.resourceClaimAllocations[claimUID]
	if !ok {
		return nil, false
	}
	requests := make(map[string]cpuset.CPUSet, len(allocation.requests))
	for name, req := range allocation.requests {
		requests[name] = req.cpus.Clone()
	}
	return requests, true
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

func (s *AllocationState) cloneEmptyLocked() *AllocationState {
	return &AllocationState{
		nodeName:                 s.nodeName,
		availableCPUs:            s.availableCPUs.Clone(),
		reservedCPUs:             s.reservedCPUs.Clone(),
		preparedCPUs:             cpuset.New(),
		resourceClaimAllocations: make(map[types.UID]claimAllocation),
		topologyFingerprint:      s.topologyFingerprint,
	}
}

func (s *AllocationState) cloneLocked() *AllocationState {
	allocations := make(map[types.UID]claimAllocation, len(s.resourceClaimAllocations))
	for uid, info := range s.resourceClaimAllocations {
		allocations[uid] = info.clone()
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
