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
