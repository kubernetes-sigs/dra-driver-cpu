/*
Copyright 2026 The Kubernetes Authors.

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
	"fmt"
	"sync"

	"github.com/go-logr/logr"
	k8stypes "k8s.io/apimachinery/pkg/types"
)

type AlreadyOwned struct {
	ClaimUID k8stypes.UID
	Owner    OwnerIdent
}

func (ao AlreadyOwned) Error() string {
	return fmt.Sprintf("claimUID %q already bound to pod %q container %q", ao.ClaimUID, ao.Owner.PodUID, ao.Owner.ContainerName)
}

type OwnerIdent struct {
	PodUID        k8stypes.UID
	ContainerName string
}

func (oi OwnerIdent) Equal(x OwnerIdent) bool {
	return oi.PodUID == x.PodUID && oi.ContainerName == x.ContainerName
}

type ClaimTracker struct {
	mu sync.Mutex
	// claimUID => podUID(+containerName) mapping.
	// No claims can be shared by containers or pods
	// But a container can have more than a claim.
	ownerByClaimUID map[k8stypes.UID]OwnerIdent
}

func NewClaimTracker() *ClaimTracker {
	return &ClaimTracker{
		ownerByClaimUID: make(map[k8stypes.UID]OwnerIdent),
	}
}

func (ctk *ClaimTracker) SetOwner(lh logr.Logger, claimUID, podUID k8stypes.UID, containerName string) error {
	curIdent := OwnerIdent{
		PodUID:        podUID,
		ContainerName: containerName,
	}
	ctk.mu.Lock()
	defer ctk.mu.Unlock()
	owner, ok := ctk.ownerByClaimUID[claimUID]
	if ok {
		if owner.Equal(curIdent) {
			lh.V(2).Info("claim bound again to the same owner", "claimUID", claimUID, "podUID", podUID, "containerName", containerName)
			return nil // not wrong, not suspicious enough to bail out
		}
		return AlreadyOwned{
			ClaimUID: claimUID,
			Owner:    owner,
		}
	}
	ctk.ownerByClaimUID[claimUID] = curIdent
	lh.V(4).Info("claim bound", "claimUID", claimUID, "podUID", podUID, "containerName", containerName)
	return nil
}

func (ctk *ClaimTracker) FindOwner(lh logr.Logger, claimUID k8stypes.UID) (OwnerIdent, bool) {
	ctk.mu.Lock()
	defer ctk.mu.Unlock()
	owner, ok := ctk.ownerByClaimUID[claimUID]
	return owner, ok
}

func (ctk *ClaimTracker) Cleanup(lh logr.Logger, claimUIDs ...k8stypes.UID) {
	ctk.mu.Lock()
	defer ctk.mu.Unlock()
	for _, claimUID := range claimUIDs {
		delete(ctk.ownerByClaimUID, claimUID)
	}
}

func (ctk *ClaimTracker) Len() int {
	ctk.mu.Lock()
	defer ctk.mu.Unlock()
	return len(ctk.ownerByClaimUID)
}
