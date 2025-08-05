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

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/cpuset"
)

// CPUType defines whether the CPU allocation is for a Guaranteed or BestEffort container.
type CPUType string

const (
	// CPUTypeGuaranteed is for containers with guaranteed CPU allocation.
	CPUTypeGuaranteed CPUType = "Guaranteed"
	// CPUTypeShared is for containers with shared CPU allocation.
	CPUTypeShared CPUType = "Shared"
)

// ContainerCPUState holds the CPU allocation type and all claim assignments for a container.
type ContainerCPUState struct {
	cpuType       CPUType
	containerName string
	containerUID  types.UID
	// updated if cpuType is CPUTypeGuaranteed
	guaranteedCPUs cpuset.CPUSet
}

// NewContainerCPUState creates a new ContainerCPUState.
func NewContainerCPUState(cpuType CPUType, containerName string, containerUID types.UID, guaranteedCPUs cpuset.CPUSet) *ContainerCPUState {
	return &ContainerCPUState{
		cpuType:        cpuType,
		containerName:  containerName,
		containerUID:   containerUID,
		guaranteedCPUs: guaranteedCPUs,
	}
}

// PodCPUAssignments maps a container name to its CPU state.
type PodCPUAssignments map[string]*ContainerCPUState

// PodConfigStore maps a Pod's UID directly to its container-level CPU assignments.
type PodConfigStore struct {
	mu         sync.RWMutex
	configs    map[types.UID]PodCPUAssignments
	allCPUs    cpuset.CPUSet
	publicCPUs cpuset.CPUSet
}

// NewPodConfigStore creates a new PodConfigStore.
func NewPodConfigStore() *PodConfigStore {
	cpuIDs := []int{}
	cpuInfo, err := cpuinfo.GetCPUInfos()
	if err != nil {
		klog.Fatalf("Fatal error getting CPU topology: %v", err)
	}
	for _, cpu := range cpuInfo {
		cpuIDs = append(cpuIDs, cpu.CpuID)
	}

	allCPUsSet := cpuset.New(cpuIDs...)

	return &PodConfigStore{
		configs:    make(map[types.UID]PodCPUAssignments),
		allCPUs:    allCPUsSet,
		publicCPUs: allCPUsSet.Clone(),
	}
}

// SetContainerState records or updates a container's CPU allocation using a state object.
func (s *PodConfigStore) SetContainerState(podUID types.UID, state *ContainerCPUState) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.configs[podUID]; !ok {
		s.configs[podUID] = make(PodCPUAssignments)
	}

	// Use the container name from the state object as the map key.
	s.configs[podUID][state.containerName] = state

	if state.cpuType == CPUTypeGuaranteed {
		// Recalculate public CPUs after any change that could affect guaranteed CPUs.
		s.recalculatePublicCPUs()
	}

	if state.cpuType == CPUTypeGuaranteed {
		klog.Infof("Set Guaranteed CPUs for PodUID:%v Container:%s guaranteedCPUs:%v", podUID, state.containerName, state.guaranteedCPUs.String())
	} else {
		klog.Infof("Set PodUID:%v Container:%s to Shared", podUID, state.containerName)
	}
}

// RemoveContainerState removes a container's state from the store.
func (s *PodConfigStore) RemoveContainerState(podUID types.UID, containerName string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	podAssignments, ok := s.configs[podUID]
	if !ok {
		return
	}

	state, ok := podAssignments[containerName]
	if !ok {
		return
	}

	klog.Infof("Removing container state for %s from PodUID %v", containerName, podUID)
	delete(podAssignments, containerName)

	// If this was the last container for the pod, clean up the pod entry itself.
	if len(podAssignments) == 0 {
		delete(s.configs, podUID)
	}

	// If a guaranteed container was removed, its CPUs must be returned to the public pool.
	if state.cpuType == CPUTypeGuaranteed {
		s.recalculatePublicCPUs()
		klog.Infof("Recalculated public CPUs after removing guaranteed container.")
	}
}

// GetContainersWithSharedCPUs returns a list of container UIDs that have shared CPU allocation.
// TODO(pravk03): Cache this and return in O(1).
func (s *PodConfigStore) GetContainersWithSharedCPUs() []types.UID {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sharedCPUContainers := []types.UID{}
	for _, podAssignments := range s.configs {
		for _, state := range podAssignments {
			if state.cpuType == CPUTypeShared {
				sharedCPUContainers = append(sharedCPUContainers, state.containerUID)
			}
		}
	}

	return sharedCPUContainers
}

// IsContainerGuaranteed checks if the container has a guaranteed CPU allocation.
func (s *PodConfigStore) IsContainerGuaranteed(podUID types.UID, containerName string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	podAssignments, ok := s.configs[podUID]
	if !ok {
		klog.Warningf("Pod with UID %v not found in PodConfigStore", podUID)
		return false
	}

	containerState, ok := podAssignments[containerName]
	if !ok {
		klog.Warningf("Container %s not found in Pod %v in PodConfigStore", containerName, podUID)
		return false
	}
	return containerState.cpuType == CPUTypeGuaranteed
}

// IsPodGuaranteed checks if any container in the pod has a guaranteed CPU allocation.
func (s *PodConfigStore) IsPodGuaranteed(podUID types.UID) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	podAssignments, ok := s.configs[podUID]
	if !ok {
		klog.Warningf("Pod with UID %v not found in PodConfigStore", podUID)
		return false
	}

	for _, state := range podAssignments {
		if state.cpuType == CPUTypeGuaranteed {
			return true
		}
	}
	return false
}

// GetPublicCPUs calculates and returns the list of CPUs not reserved by any Guaranteed containers.
// This is the pool available for Shared containers.
func (s *PodConfigStore) GetPublicCPUs() cpuset.CPUSet {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.publicCPUs
}

func (s *PodConfigStore) recalculatePublicCPUs() {
	guaranteedCPUs := cpuset.New()
	for _, podAssignments := range s.configs {
		for _, state := range podAssignments {
			if state.cpuType == CPUTypeGuaranteed {
				guaranteedCPUs = guaranteedCPUs.Union(state.guaranteedCPUs)
			}
		}
	}
	s.publicCPUs = s.allCPUs.Difference(guaranteedCPUs)
}

// DeletePodState removes a pod's state from the store.
func (s *PodConfigStore) DeletePodState(podUID types.UID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.configs[podUID]; !ok {
		return
	}
	recalculatePublicCPUs := false
	for _, state := range s.configs[podUID] {
		if state.cpuType == CPUTypeGuaranteed {
			recalculatePublicCPUs = true
			break
		}
	}
	delete(s.configs, podUID)
	if recalculatePublicCPUs {
		s.recalculatePublicCPUs()
	}
}
