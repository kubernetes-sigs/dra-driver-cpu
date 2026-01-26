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
package driver

import (
	"sync"

	"k8s.io/apimachinery/pkg/types"
)

// ContainerState holds the allocation type and all claim assignments for a container.
type ContainerState struct {
	// containerName is used as the primary key for efficient lookups within the PodConfigStore.
	containerName string
	// containerUID is used by the container runtime to apply updates to a container.
	containerUID types.UID
	// resourceClaimUIDs is a list of resource claims associated with this container.
	resourceClaimUIDs []types.UID
}

// NewContainerState creates a new ContainerState.
func NewContainerState(containerName string, containerUID types.UID, claimUIDs ...types.UID) *ContainerState {
	return &ContainerState{
		containerName:     containerName,
		containerUID:      containerUID,
		resourceClaimUIDs: claimUIDs,
	}
}

// PodCPUAssignments maps a container name to its state.
type PodCPUAssignments map[string]*ContainerState

// PodConfigStore maps a Pod's UID directly to its container-level assignments.
type PodConfigStore struct {
	mu      sync.RWMutex
	configs map[types.UID]PodCPUAssignments
}

// NewPodConfigStore creates a new PodConfigStore.
func NewPodConfigStore() *PodConfigStore {
	return &PodConfigStore{
		configs: make(map[types.UID]PodCPUAssignments),
	}
}

// SetContainerState records or updates a container's allocation using a state object.
func (s *PodConfigStore) SetContainerState(podUID types.UID, state *ContainerState) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.configs[podUID]; !ok {
		s.configs[podUID] = make(PodCPUAssignments)
	}
	s.configs[podUID][state.containerName] = state
}

// GetContainerState retrieves a container's state.
func (s *PodConfigStore) GetContainerState(podUID types.UID, containerName string) *ContainerState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if podAssignments, ok := s.configs[podUID]; ok {
		return podAssignments[containerName]
	}
	return nil
}

// RemoveContainerState removes a container's state from the store.
func (s *PodConfigStore) RemoveContainerState(podUID types.UID, containerName string) []types.UID {
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

	claimUIDs := cs.resourceClaimUIDs
	if claimUIDs == nil {
		claimUIDs = []types.UID{}
	}

	delete(podAssignments, containerName)
	if len(podAssignments) == 0 {
		delete(s.configs, podUID)
	}

	return claimUIDs
}

// GetContainersWithSharedCPUs returns a list of container UIDs that have shared CPU allocation.
// TODO(pravk03): Cache this and return from this function in O(1)
func (s *PodConfigStore) GetContainersWithSharedCPUs() []types.UID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sharedCPUContainers := []types.UID{}
	for _, podAssignments := range s.configs {
		for _, state := range podAssignments {
			if len(state.resourceClaimUIDs) == 0 {
				sharedCPUContainers = append(sharedCPUContainers, state.containerUID)
			}
		}
	}
	return sharedCPUContainers
}
