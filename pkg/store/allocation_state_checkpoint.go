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

// Heavily inspred by and modeled after Kubernetes CPU Manager
// checkpoint V4 as k/k @ 975cf3742dec751d579f525c490b281fe927fa5d
package store

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/checkpoint"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/cpuset"
)

const (
	allocationStateCheckpointVersion = 1
	allocationCheckpointName         = "allocation_state"
)

var (
	ErrNilCheckpointManager = errors.New("checkpoint manager is nil")
	ErrBootIDUnavailable    = errors.New("kernel boot ID unavailable; ensure the host procfs boot_id is readable")
	ErrTopologyChanged      = errors.New("current machine topology differs from checkpointed machine topology")
	ErrNodeChanged          = errors.New("current node differs from checkpointed node")
)

type ownerIdentifierCheckpoint struct {
	PodUID        string `cbor:"podUID"`
	ContainerName string `cbor:"containerName"`
}

type requestAllocationCheckpoint struct {
	CPUs   string                      `cbor:"cpus"`
	Owners []ownerIdentifierCheckpoint `cbor:"owners"`
}

type namespacedNameCheckpoint struct {
	Namespace string `cbor:"namespace"`
	Name      string `cbor:"name"`
}

type claimAllocationCheckpoint struct {
	Claim    namespacedNameCheckpoint               `cbor:"claim"`
	CPUs     string                                 `cbor:"cpus"`
	Requests map[string]requestAllocationCheckpoint `cbor:"requests"`
}

type allocationStateCheckpointMeta struct {
	Version             int    `cbor:"version"`
	NodeName            string `cbor:"nodeName"`
	ReservedCPUs        string `cbor:"reservedCPUs"`
	TopologyFingerprint string `cbor:"topologyFingerprint"`
	BootID              string `cbor:"bootID"`
}

type allocationStateCheckpointPayload struct {
	ClaimInfo map[string]claimAllocationCheckpoint `cbor:"claimInfo"`
}

type allocationStateCheckpointData struct {
	Meta    allocationStateCheckpointMeta    `cbor:"meta"`
	Payload allocationStateCheckpointPayload `cbor:"payload"`
}

type AllocationStateCheckpoint struct {
	Data         []byte              `cbor:"data"`
	DataChecksum checkpoint.Checksum `cbor:"dataChecksum"`

	checkpointData allocationStateCheckpointData `cbor:"-"`
}

var _ checkpoint.Checkpoint = (*AllocationStateCheckpoint)(nil)

func newAllocationStateCheckpoint() *AllocationStateCheckpoint {
	return &AllocationStateCheckpoint{
		checkpointData: allocationStateCheckpointData{
			Payload: allocationStateCheckpointPayload{
				ClaimInfo: make(map[string]claimAllocationCheckpoint),
			},
		},
	}
}

// LoadState replaces the state with the checkpointed one. It returns false
// when there is no usable checkpoint (missing, or from a previous boot); the
// state is left untouched in that case.
func (s *AllocationState) LoadState(logger logr.Logger, checkpointMgr checkpoint.Manager) (bool, error) {
	if checkpointMgr == nil {
		return false, ErrNilCheckpointManager
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadStateLocked(logger, checkpointMgr)
}

func (s *AllocationState) loadStateLocked(logger logr.Logger, checkpointMgr checkpoint.Manager) (bool, error) {
	bootID := checkpointMgr.GetBootID()
	if bootID == "" {
		return false, ErrBootIDUnavailable
	}

	cp := newAllocationStateCheckpoint()
	if err := checkpointMgr.GetCheckpoint(allocationCheckpointName, cp); err != nil {
		if errors.Is(err, checkpoint.ErrCheckpointNotFound) {
			return false, nil
		}
		return false, err
	}

	if cp.checkpointData.Meta.BootID != bootID {
		logger.Info("ignoring checkpoint because boot IDs differ", "current", bootID, "checkpointed", cp.checkpointData.Meta.BootID)
		return false, nil
	}

	// GetCheckpoint validated the checkpoint transport integrity. Now we need to validate
	// the checkpoint semantic validity
	if cp.checkpointData.Meta.TopologyFingerprint != s.topologyFingerprint {
		return false, ErrTopologyChanged
	}
	if cp.checkpointData.Meta.NodeName != s.nodeName {
		return false, ErrNodeChanged
	}

	checkpointedCPUs, err := cpuset.Parse(cp.checkpointData.Meta.ReservedCPUs)
	if err != nil {
		return false, fmt.Errorf("cannot recover checkpointed reserved cpus: %w", err)
	}
	if !checkpointedCPUs.Equals(s.reservedCPUs) {
		logger.V(2).Info("reserved CPUs changed since checkpoint",
			"checkpointedReservedCPUs", checkpointedCPUs.String(),
			"configuredReservedCPUs", s.reservedCPUs.String())
	}

	loaded := 0
	candidate := s.cloneEmptyLocked()
	for claimIdent, claimData := range cp.checkpointData.Payload.ClaimInfo {
		if claimIdent == "" {
			return false, errors.New("restoring claim: missing ident")
		}
		// claimData.Claim may be missing, and it's tolerable

		// fast checks first
		if claimData.CPUs == "" {
			return false, fmt.Errorf("restoring claim %q: malformed claim data cpus=%s", claimIdent, claimData.CPUs)
		}
		cpus, err := cpuset.Parse(claimData.CPUs)
		if err != nil {
			return false, fmt.Errorf("restoring claim %q: %w", claimIdent, err)
		}
		if cpus.IsEmpty() {
			return false, fmt.Errorf("restoring claim %q: CPUs are empty", claimIdent)
		}
		// reserved CPU can change across driver restarts. We intentionally allow this change, logging only.
		// Therefore, we can "just" validate is that all the claims are against CPUs in the available (all - reserved) set.
		if !cpus.IsSubsetOf(s.availableCPUs) {
			return false, fmt.Errorf("restoring claim %q: CPUs outside the available set: %s", claimIdent, claimData.CPUs)
		}

		requests := make(map[string]requestAllocation)
		for name, alloc := range claimData.Requests {
			owners := sets.New[OwnerIdentifier]()
			reqCpus, err := cpuset.Parse(alloc.CPUs)
			if err != nil {
				return false, fmt.Errorf("restoring claim %q request %q: %w", claimIdent, name, err)
			}
			for _, reqOwner := range alloc.Owners {
				owner := OwnerIdentifier{
					PodUID:        types.UID(reqOwner.PodUID),
					ContainerName: reqOwner.ContainerName,
				}
				if !owner.Assigned() {
					return false, fmt.Errorf("restoring claim %q request %q: owner must be assigned", claimIdent, name)
				}
				if owners.Has(owner) {
					return false, fmt.Errorf("restoring claim %q request %q: duplicate owner %q", claimIdent, name, owner.String())
				}
				owners.Insert(owner)
			}
			ownerList := owners.UnsortedList()
			// stable sorting to get a stabler serialization
			slices.SortFunc(ownerList, func(a, b OwnerIdentifier) int {
				if n := strings.Compare(string(a.PodUID), string(b.PodUID)); n != 0 {
					return n
				}
				return cmp.Compare(a.ContainerName, b.ContainerName)
			})
			requests[name] = requestAllocation{
				cpus:   reqCpus,
				owners: ownerList,
			}
		}

		ca := claimAllocation{
			claim: types.NamespacedName{
				Namespace: claimData.Claim.Namespace,
				Name:      claimData.Claim.Name,
			},
		}
		if err := fillClaimAllocationFromRequests(&ca, maps.All(requests), len(requests)); err != nil {
			return false, fmt.Errorf("restoring claim %q cpus: %w", claimIdent, err)
		}
		if !ca.cpus.Equals(cpus) {
			return false, fmt.Errorf("restoring claim %q: request CPUs %q differ from claim CPUs %q", claimIdent, ca.cpus.String(), cpus.String())
		}
		overlap := candidate.preparedCPUs.Intersection(cpus)
		if !overlap.IsEmpty() {
			return false, fmt.Errorf("restoring claim %q cpus: overlap with already prepared CPUs %s", claimIdent, overlap.String())
		}
		candidate.resourceClaimAllocations[types.UID(claimIdent)] = ca
		candidate.preparedCPUs = candidate.preparedCPUs.Union(cpus)
		loaded++
	}

	s.applyLocked(candidate)
	logger.V(4).Info("load state", "allocationCount", loaded, "preparedCPUs", candidate.preparedCPUs.String())
	return true, nil
}

func (s *AllocationState) StoreState(logger logr.Logger, checkpointMgr checkpoint.Manager) error {
	if checkpointMgr == nil {
		return ErrNilCheckpointManager
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.storeStateLocked(logger, checkpointMgr)
}

func (s *AllocationState) storeStateLocked(logger logr.Logger, checkpointMgr checkpoint.Manager) error {
	if checkpointMgr == nil {
		return ErrNilCheckpointManager
	}
	bootID := checkpointMgr.GetBootID()
	if bootID == "" {
		return ErrBootIDUnavailable
	}
	if err := checkpointMgr.CreateCheckpoint(allocationCheckpointName, s.checkpointLocked(bootID)); err != nil {
		return fmt.Errorf("failed to write allocation state checkpoint: %w", err)
	}
	logger.V(6).Info("checkpoint created")
	return nil
}

func makeRequestAllocationCheckpoint(req requestAllocation) requestAllocationCheckpoint {
	owners := make([]ownerIdentifierCheckpoint, 0, len(req.owners))
	for _, owner := range req.owners {
		owners = append(owners, ownerIdentifierCheckpoint{
			PodUID:        string(owner.PodUID),
			ContainerName: owner.ContainerName,
		})
	}
	// ensure stable serialization
	slices.SortFunc(owners, func(a, b ownerIdentifierCheckpoint) int {
		if n := strings.Compare(a.PodUID, b.PodUID); n != 0 {
			return n
		}
		return strings.Compare(a.ContainerName, b.ContainerName)
	})
	return requestAllocationCheckpoint{
		CPUs:   req.cpus.String(),
		Owners: owners,
	}
}

func (s *AllocationState) checkpointLocked(bootID string) *AllocationStateCheckpoint {
	claimInfo := make(map[string]claimAllocationCheckpoint, len(s.resourceClaimAllocations))
	for claimUID, allocation := range s.resourceClaimAllocations {
		reqs := make(map[string]requestAllocationCheckpoint, len(allocation.requests))
		for name, alloc := range allocation.requests {
			reqs[name] = makeRequestAllocationCheckpoint(alloc)
		}

		claimInfo[string(claimUID)] = claimAllocationCheckpoint{
			Claim: namespacedNameCheckpoint{
				Namespace: allocation.claim.Namespace,
				Name:      allocation.claim.Name,
			},
			CPUs:     allocation.cpus.String(),
			Requests: reqs,
		}
	}
	return &AllocationStateCheckpoint{
		checkpointData: allocationStateCheckpointData{
			Meta: allocationStateCheckpointMeta{
				Version:             allocationStateCheckpointVersion,
				NodeName:            s.nodeName,
				ReservedCPUs:        s.reservedCPUs.String(),
				TopologyFingerprint: s.topologyFingerprint,
				BootID:              bootID,
			},
			Payload: allocationStateCheckpointPayload{
				ClaimInfo: claimInfo,
			},
		},
	}
}

// MarshalCheckpoint serializes the checkpoint in the V4-style format.
func (cp *AllocationStateCheckpoint) MarshalCheckpoint() ([]byte, error) {
	data, err := cbor.Marshal(cp.checkpointData)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize allocation state checkpoint data: %w", err)
	}
	cp.Data = data
	// make sure to checksum the serialized payload for extensibility
	cp.DataChecksum = checkpoint.NewChecksum(cp.Data)
	return cbor.Marshal(*cp)
}

// UnmarshalCheckpoint deserializes the outer checkpoint and its nested data.
func (cp *AllocationStateCheckpoint) UnmarshalCheckpoint(blob []byte) error {
	if err := cbor.Unmarshal(blob, cp); err != nil {
		return fmt.Errorf("failed to deserialize allocation state checkpoint: %w", err)
	}
	if err := cbor.Unmarshal(cp.Data, &cp.checkpointData); err != nil {
		return fmt.Errorf("failed to deserialize allocation state checkpoint data: %w", err)
	}
	if cp.checkpointData.Meta.Version != allocationStateCheckpointVersion {
		return fmt.Errorf("unsupported allocation state checkpoint version %d", cp.checkpointData.Meta.Version)
	}
	return nil
}

// VerifyChecksum verifies the authoritative V4-style payload checksum.
func (cp *AllocationStateCheckpoint) VerifyChecksum() error {
	return cp.DataChecksum.Verify(cp.Data)
}
