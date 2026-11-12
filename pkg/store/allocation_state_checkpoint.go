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
	allocationCheckpointName         = "allocation_state"
)

var (
	ErrNilCheckpointManager = errors.New("checkpoint manager is nil")
	ErrBootIDUnavailable    = errors.New("kernel boot ID unavailable; ensure the host procfs boot_id is readable")
	ErrTopologyChanged      = errors.New("current machine topology differs from checkpointed machine topology")
	ErrNodeChanged          = errors.New("current node differs from checkpointed node")
)

type ownerIdentCheckpoint struct {
	PodUID        string `cbor:"podUID"`
	ContainerName string `cbor:"containerName"`
}

type namespacedNameCheckpoint struct {
	Namespace string `cbor:"namespace"`
	Name      string `cbor:"name"`
}

type claimAllocationCheckpoint struct {
	Claim  namespacedNameCheckpoint `cbor:"claim"`
	CPUs   string                   `cbor:"cpus"`
	Owners []ownerIdentCheckpoint   `cbor:"owners"`
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

func (s *AllocationState) LoadState(logger logr.Logger, checkpointMgr checkpoint.Manager) error {
	if checkpointMgr == nil {
		return ErrNilCheckpointManager
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadStateLocked(logger, checkpointMgr)
}

func (s *AllocationState) loadStateLocked(logger logr.Logger, checkpointMgr checkpoint.Manager) error {
	bootID := checkpointMgr.GetBootID()
	if bootID == "" {
		return ErrBootIDUnavailable
	}

	cp := newAllocationStateCheckpoint()
	if err := checkpointMgr.GetCheckpoint(allocationCheckpointName, cp); err != nil {
		if errors.Is(err, checkpoint.ErrCheckpointNotFound) {
			return nil
		}
		return err
	}

	if cp.checkpointData.Meta.BootID != bootID {
		logger.Info("ignoring checkpoint because boot IDs differ", "current", bootID, "checkpointed", cp.checkpointData.Meta.BootID)
		return nil
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
		logger.V(2).Info("reserved CPUs changed since checkpoint",
			"checkpointedReservedCPUs", checkpointedCPUs.String(),
			"configuredReservedCPUs", s.reservedCPUs.String())
	}

	loaded := 0
	candidate := s.cloneLocked()
	for claimIdent, claimData := range cp.checkpointData.Payload.ClaimInfo {
		if claimIdent == "" {
			return errors.New("restoring claim: missing ident")
		}
		if claimData.CPUs == "" {
			return fmt.Errorf("restoring claim %q: malformed claim data cpus=%s", claimIdent, claimData.CPUs)
		}
		// The wire format supports sharing before the runtime does; never discard owners.
		if len(claimData.Owners) > 1 {
			return fmt.Errorf("restoring claim %q: %d owners are not supported by this version of the driver", claimIdent, len(claimData.Owners))
		}
		var owner OwnerIdent
		if len(claimData.Owners) == 1 {
			owner = OwnerIdent{
				PodUID:        types.UID(claimData.Owners[0].PodUID),
				ContainerName: claimData.Owners[0].ContainerName,
			}
			if !owner.Assigned() {
				return fmt.Errorf("restoring claim %q: owner must provide both a pod UID and container name", claimIdent)
			}
		}
		cpus, err := cpuset.Parse(claimData.CPUs)
		if err != nil {
			return fmt.Errorf("restoring claim %q (owner=%s): %w", claimIdent, owner.String(), err)
		}
		if cpus.IsEmpty() {
			return fmt.Errorf("restoring claim %q (owner=%s): CPUs are empty", claimIdent, owner.String())
		}
		if !cpus.IsSubsetOf(s.availableCPUs) {
			return fmt.Errorf("restoring claim %q (owner=%s): CPUs outside the available set: %s", claimIdent, owner.String(), claimData.CPUs)
		}
		candidate.resourceClaimAllocations[types.UID(claimIdent)] = claimAllocation{
			claim: types.NamespacedName{
				Namespace: claimData.Claim.Namespace,
				Name:      claimData.Claim.Name,
			},
			cpus:  cpus,
			owner: owner,
		}
		overlap := candidate.preparedCPUs.Intersection(cpus)
		if !overlap.IsEmpty() {
			return fmt.Errorf("restoring claim %q cpus (owner=%s): overlap with already prepared CPUs %s", claimIdent, owner.String(), overlap.String())
		}
		candidate.preparedCPUs = candidate.preparedCPUs.Union(cpus)
		loaded++
	}

	loadedCPUs := candidate.preparedCPUs.Difference(s.preparedCPUs)
	s.applyLocked(candidate)
	logger.V(4).Info("load state", "allocationCount", loaded, "preparedCPUs", loadedCPUs.String())
	return nil
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

func (s *AllocationState) checkpointLocked(bootID string) *AllocationStateCheckpoint {
	claimInfo := make(map[string]claimAllocationCheckpoint, len(s.resourceClaimAllocations))
	for claimUID, allocation := range s.resourceClaimAllocations {
		var owners []ownerIdentCheckpoint
		if !allocation.owner.Vacant() {
			owners = []ownerIdentCheckpoint{
				{
					PodUID:        string(allocation.owner.PodUID),
					ContainerName: allocation.owner.ContainerName,
				},
			}
		}
		claimInfo[string(claimUID)] = claimAllocationCheckpoint{
			Claim: namespacedNameCheckpoint{
				Namespace: allocation.claim.Namespace,
				Name:      allocation.claim.Name,
			},
			CPUs:   allocation.cpus.String(),
			Owners: owners,
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
