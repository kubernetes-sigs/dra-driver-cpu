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
)

const (
	allocationStateCheckpointVersion = 1
	allocationCheckpointName         = "alloc.db"
)

type claimAllocationCheckpoint struct {
	CPUs  string     `json:"cpus"`
	Owner OwnerIdent `json:"owner"`
}

type allocationStateCheckpointMeta struct {
	Version int `json:"version"`
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
			CPUs:  allocation.cpus.String(),
			Owner: allocation.owner,
		}
	}
	return &AllocationStateCheckpoint{
		checkpointData: allocationStateCheckpointData{
			Meta: allocationStateCheckpointMeta{
				Version: allocationStateCheckpointVersion,
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
