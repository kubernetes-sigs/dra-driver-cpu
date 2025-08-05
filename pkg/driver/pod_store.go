/*
Copyright 2024 The Kubernetes Authors.

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

// PodCPUAssignments maps a container name to its CPU state.
type PodCPUAssignments map[string]*ContainerCPUState

// PodConfigStore maps a Pod's UID directly to its container-level CPU assignments.
type PodConfigStore struct {
	mu         sync.RWMutex
	configs    map[types.UID]PodCPUAssignments
	allCPUs    cpuset.CPUSet
	publicCPUs cpuset.CPUSet
}

func NewPodConfigStore() *PodConfigStore {
	return &PodConfigStore{}
}
