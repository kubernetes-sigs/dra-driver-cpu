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
	"fmt"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
)

type mockCPUInfoProvider struct {
	cpuInfos []cpuinfo.CPUInfo
	err      error
}

func (m *mockCPUInfoProvider) GetCPUInfos() ([]cpuinfo.CPUInfo, error) {
	return m.cpuInfos, m.err
}

func (m *mockCPUInfoProvider) GetCPUTopology() (*cpuinfo.CPUTopology, error) {
	cpuDetails := make(cpuinfo.CPUDetails)
	sockets := make(map[int]struct{})
	numaNodes := make(map[int]struct{})
	cores := make(map[string]struct{})
	l3Caches := make(map[int64]struct{})

	for i := range m.cpuInfos {
		info := m.cpuInfos[i]
		cpuDetails[info.CpuID] = info
		sockets[info.SocketID] = struct{}{}
		numaNodes[info.NUMANodeID] = struct{}{}
		// A core is unique by socket and core id
		coreKey := fmt.Sprintf("%d-%d", info.SocketID, info.CoreID)
		cores[coreKey] = struct{}{}
		if info.L3CacheID != -1 {
			l3Caches[info.L3CacheID] = struct{}{}
		}
	}

	return &cpuinfo.CPUTopology{
		NumCPUs:        len(m.cpuInfos),
		NumCores:       len(cores),
		NumSockets:     len(sockets),
		NumNUMANodes:   len(numaNodes),
		NumUncoreCache: len(l3Caches),
		CPUDetails:     cpuDetails,
	}, m.err
}
