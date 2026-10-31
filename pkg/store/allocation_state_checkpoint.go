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

// Taken by  Kubernetes CPU Manager checkpoint V4 as k/k @ 975cf3742dec751d579f525c490b281fe927fa5d
package store

import (
	"errors"
	"fmt"

	"github.com/fxamacker/cbor/v2"
	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/checkpoint"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/cpuset"
)

const (
	allocationStateCheckpointVersion = 1
	allocationCheckpointName         = "alloc.db"
)

var (
	ErrTopologyChanged = errors.New("current machine topology differs from checkpointed machine topology")
	ErrNodeChanged     = errors.New("current node differs from checkpointed node")
	ErrReservedChanged = errors.New("configured reserved CPUs differ from checkpointed reserved CPUs")
)

type claimAllocationCheckpoint struct {
	Claim NamespacedName `json:"claim"`
	CPUs  string         `json:"cpus"`
	Owner OwnerIdent     `json:"owner"`
}

type allocationStateCheckpointMeta struct {
	Version             int    `json:"version"`
	NodeName            string `json:"nodeName"`
	ReservedCPUs        string `json:"reservedCPUs"`
	TopologyFingerprint string `json:"topologyFingerprint"`
}

type allocationStateCheckpointPayload struct {
	ClaimInfo map[string]claimAllocationCheckpoint `json:"claimInfo"`
}

type allocationStateCheckpointData struct {
	Meta    allocationStateCheckpointMeta    `json:"meta"`
	Payload allocationStateCheckpointPayload `json:"payload"`
}

type AllocationStateCheckpoint struct {
	Data         []byte              `json:"data"`
	DataChecksum checkpoint.Checksum `json:"dataChecksum"`

	checkpointData allocationStateCheckpointData `json:"-"`
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

func (s *AllocationState) LoadState(logger logr.Logger, checkpointMgr checkpoint.Manager) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadStateLocked(logger, checkpointMgr)
}

func (s *AllocationState) loadStateLocked(logger logr.Logger, checkpointMgr checkpoint.Manager) error {
	cp := newAllocationStateCheckpoint()
	if err := checkpointMgr.GetCheckpoint(allocationCheckpointName, cp); err != nil {
		if errors.Is(err, checkpoint.ErrCheckpointNotFound) {
			return nil
		}
		return err
	}

	// GetCheckpoint validated the checkpoint transport integrity. Now we need to validate
	// the checkpoint semantic validity
	if cp.checkpointData.Meta.TopologyFingerprint != s.topologyFingerprint {
		return ErrTopologyChanged
	}
	if cp.checkpointData.Meta.NodeName != s.nodeName {
		return ErrNodeChanged
	}

	checkpointedCPUs, err := cpuset.Parse(cp.checkpointData.Meta.ReservedCPUs)
	if err != nil {
		return fmt.Errorf("cannot recover checkpointed reserved cpus: %w", err)
	}
	if !checkpointedCPUs.Equals(s.reservedCPUs) {
		return fmt.Errorf("%w: checkpointed %s, configured %s",
			ErrReservedChanged,
			cp.checkpointData.Meta.ReservedCPUs,
			s.reservedCPUs.String())
	}

	candidate := s.cloneLocked()
	for claimIdent, claimData := range cp.checkpointData.Payload.ClaimInfo {
		if claimIdent == "" {
			return errors.New("restoring claim: missing ident")
		}
		if claimData.CPUs == "" {
			return fmt.Errorf("restoring claim %q: malformed claim data cpus=%s", claimIdent, claimData.CPUs)
		}
		if !claimData.Owner.Vacant() && !claimData.Owner.Assigned() {
			return fmt.Errorf("restoring claim %q: claim must be vacant or assigned (current %s)", claimIdent, claimData.Owner.String())
		}
		cpus, err := cpuset.Parse(claimData.CPUs)
		if err != nil {
			return fmt.Errorf("restoring claim %q (owner=%s): %w", claimIdent, claimData.Owner.String(), err)
		}
		if cpus.IsEmpty() {
			return fmt.Errorf("restoring claim %q (owner=%s): CPUs are empty", claimIdent, claimData.Owner.String())
		}
		if !cpus.IsSubsetOf(s.availableCPUs) {
			return fmt.Errorf("restoring claim %q (owner=%s): CPUs outside the available set: %s", claimIdent, claimData.Owner.String(), claimData.CPUs)
		}
		candidate.resourceClaimAllocations[types.UID(claimIdent)] = claimAllocation{
			claim: claimData.Claim,
			cpus:  cpus,
			owner: claimData.Owner,
		}
		overlap := candidate.preparedCPUs.Intersection(cpus)
		if !overlap.IsEmpty() {
			return fmt.Errorf("restoring claim %q cpus (owner=%s): overlap with already prepared CPUs %s", claimIdent, claimData.Owner.String(), overlap.String())
		}
		candidate.preparedCPUs = candidate.preparedCPUs.Union(cpus)
	}

	s.resourceClaimAllocations = candidate.resourceClaimAllocations
	s.preparedCPUs = candidate.preparedCPUs
	return nil
}

func (s *AllocationState) storeStateLocked(logger logr.Logger, checkpointMgr checkpoint.Manager) error {
	if checkpointMgr == nil {
		return errors.New("checkpoint manager is nil")
	}
	if err := checkpointMgr.CreateCheckpoint(allocationCheckpointName, s.checkpointLocked()); err != nil {
		return fmt.Errorf("failed to write allocation state checkpoint: %w", err)
	}
	return nil
}

func (s *AllocationState) checkpointLocked() *AllocationStateCheckpoint {
	claimInfo := make(map[string]claimAllocationCheckpoint, len(s.resourceClaimAllocations))
	for claimUID, allocation := range s.resourceClaimAllocations {
		claimInfo[string(claimUID)] = claimAllocationCheckpoint{
			Claim: allocation.claim,
			CPUs:  allocation.cpus.String(),
			Owner: allocation.owner,
		}
	}
	return &AllocationStateCheckpoint{
		checkpointData: allocationStateCheckpointData{
			Meta: allocationStateCheckpointMeta{
				Version:             allocationStateCheckpointVersion,
				NodeName:            s.nodeName,
				ReservedCPUs:        s.reservedCPUs.String(),
				TopologyFingerprint: s.topologyFingerprint,
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
