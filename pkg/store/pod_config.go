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
	"sync"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/cpuset"
)

// AllocationType defines the high-level CPU allocation category for a container.
type AllocationType string

const (
	// AllocationTypeShared indicates a container running entirely on the node's shared CPU pool.
	AllocationTypeShared AllocationType = "shared"
	// AllocationTypeGuaranteed indicates a container with dedicated exclusive CPU resource (requested through claims) allocated via DRA claims.
	AllocationTypeGuaranteed AllocationType = "guaranteed"
	// AllocationTypeMixed indicates a container running on both dedicated exclusive CPU resources (requested through claims) and the shared CPU pool.
	AllocationTypeMixed AllocationType = "mixed"
)

// ClaimInfo holds information about a single resource claim's allocation.
type ClaimInfo struct {
	ClaimUID types.UID
	CPUs     cpuset.CPUSet
}

// ContainerState holds the allocation type and all claim assignments for a container.
type ContainerState struct {
	// containerName is used as the primary key for efficient lookups within the PodConfig.
	containerName string
	// containerUID is used by the container runtime to apply updates to a container.
	containerUID types.UID
	// claims holds information about resource claims allocated to this container.
	claims []ClaimInfo
	// allocationType indicates the high-level CPU allocation category for this container.
	allocationType AllocationType
}

// NewContainerState creates a new ContainerState.
func NewContainerState(containerName string, containerUID types.UID, allocType AllocationType, claims ...ClaimInfo) *ContainerState {
	return &ContainerState{
		containerName:  containerName,
		containerUID:   containerUID,
		claims:         claims,
		allocationType: allocType,
	}
}

// PodCPUAssignments maps a container name to its state.
type PodCPUAssignments map[string]*ContainerState

// PodConfig maps a Pod's UID directly to its container-level assignments.
type PodConfig struct {
	mu                  sync.RWMutex
	configs             map[types.UID]PodCPUAssignments
	containersByUID     map[types.UID]*ContainerState
	sharedCPUContainers sets.Set[types.UID]
	mixedCPUContainers  sets.Set[types.UID]
}

// NewPodConfig creates a new PodConfig.
func NewPodConfig() *PodConfig {
	return &PodConfig{
		configs:             make(map[types.UID]PodCPUAssignments),
		containersByUID:     make(map[types.UID]*ContainerState),
		sharedCPUContainers: sets.New[types.UID](),
		mixedCPUContainers:  sets.New[types.UID](),
	}
}

// SetContainerState records or updates a container's allocation using a state object.
func (s *PodConfig) SetContainerState(podUID types.UID, state *ContainerState) {
	s.mu.Lock()
	defer s.mu.Unlock()

	podAssignments, ok := s.configs[podUID]
	if !ok {
		podAssignments = make(PodCPUAssignments)
		s.configs[podUID] = podAssignments
	}

	if oldState, exists := podAssignments[state.containerName]; exists {
		delete(s.containersByUID, oldState.containerUID)
		s.sharedCPUContainers.Delete(oldState.containerUID)
		s.mixedCPUContainers.Delete(oldState.containerUID)
	}

	podAssignments[state.containerName] = state
	s.containersByUID[state.containerUID] = state

	switch state.allocationType {
	case AllocationTypeShared:
		s.sharedCPUContainers.Insert(state.containerUID)
	case AllocationTypeMixed:
		s.mixedCPUContainers.Insert(state.containerUID)
	}
}

// GetContainerStateByUID retrieves a container's state by its runtime UID in O(1) time.
func (s *PodConfig) GetContainerStateByUID(containerUID types.UID) (*ContainerState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	cs, ok := s.containersByUID[containerUID]
	return cs, ok
}

// RemoveContainerState removes a container's state from the store.
func (s *PodConfig) RemoveContainerState(podUID types.UID, containerName string) []types.UID {
	s.mu.Lock()
	defer s.mu.Unlock()

	podAssignments, ok := s.configs[podUID]
	if !ok {
		return []types.UID{}
	}

	cs, ok := podAssignments[containerName]
	if !ok {
		return []types.UID{}
	}

	claimUIDs := cs.GetResourceClaimUIDs()

	delete(s.containersByUID, cs.containerUID)
	s.sharedCPUContainers.Delete(cs.containerUID)
	s.mixedCPUContainers.Delete(cs.containerUID)

	delete(podAssignments, containerName)
	if len(podAssignments) == 0 {
		delete(s.configs, podUID)
	}

	return claimUIDs
}

// GetContainersWithSharedCPUs returns a list of container UIDs that have shared CPU allocation.
// The caller can specify whether to include mixed-allocation containers.
func (s *PodConfig) GetContainersWithSharedCPUs(includeMixed bool) []types.UID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	list := s.sharedCPUContainers.UnsortedList()
	if includeMixed {
		list = append(list, s.mixedCPUContainers.UnsortedList()...)
	}
	return list
}

func (s *PodConfig) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.configs)
}

// Type returns the high-level CPU allocation category of the container.
func (cs *ContainerState) Type() AllocationType {
	return cs.allocationType
}

// GetResourceClaimUIDs returns the list of resource claim UIDs.
func (cs *ContainerState) GetResourceClaimUIDs() []types.UID {
	var uids []types.UID
	for _, claim := range cs.claims {
		uids = append(uids, claim.ClaimUID)
	}
	return uids
}

// GetContainerName returns the container name.
func (cs *ContainerState) GetContainerName() string {
	return cs.containerName
}

// GetExclusiveCPUs retrieves the stored exclusive CPU set.
func (cs *ContainerState) GetExclusiveCPUs() cpuset.CPUSet {
	exclusiveCPUs := cpuset.New()
	for _, claim := range cs.claims {
		exclusiveCPUs = exclusiveCPUs.Union(claim.CPUs)
	}
	return exclusiveCPUs
}
