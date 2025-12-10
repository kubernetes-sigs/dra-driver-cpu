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

package cpuinfo

import (
	"fmt"
)

type MockCPUInfoProvider struct {
	CPUInfos []CPUInfo
	Err      error
}

func (m *MockCPUInfoProvider) GetCPUInfos() ([]CPUInfo, error) {
	return m.CPUInfos, m.Err
}

func (m *MockCPUInfoProvider) GetCPUTopology() (*CPUTopology, error) {
	cpuDetails := make(CPUDetails)
	sockets := make(map[int]struct{})
	numaNodes := make(map[int]struct{})
	cores := make(map[string]struct{})
	uncoreCaches := make(map[int]struct{})

	for i := range m.CPUInfos {
		info := m.CPUInfos[i]
		cpuDetails[info.CpuID] = info
		sockets[info.SocketID] = struct{}{}
		numaNodes[info.NUMANodeID] = struct{}{}
		// A core is unique by socket and core id
		coreKey := fmt.Sprintf("%d-%d", info.SocketID, info.CoreID)
		cores[coreKey] = struct{}{}
		if info.UncoreCacheID != -1 {
			uncoreCaches[info.UncoreCacheID] = struct{}{}
		}
	}

	return &CPUTopology{
		NumCPUs:        len(m.CPUInfos),
		NumCores:       len(cores),
		NumSockets:     len(sockets),
		NumNUMANodes:   len(numaNodes),
		NumUncoreCache: len(uncoreCaches),
		CPUDetails:     cpuDetails,
	}, m.Err
}
