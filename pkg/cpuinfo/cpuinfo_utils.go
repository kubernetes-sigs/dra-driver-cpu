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
	"k8s.io/utils/cpuset"
)

// The functions in this file provide an interface for pkg/cpumanager/cpu_assignment.go
// to query CPU topology information. These methods are adapted from the topology management in Kubernetes.
// Original source: https://github.com/kubernetes/kubernetes/blob/9998041e0ffe0dd3f2abab3b9f95505c4402bf14/pkg/kubelet/cm/cpumanager/topology/topology.go
// Based on commit: https://github.com/kubernetes/kubernetes/commit/fd5b2efa76e44c5ef523cd0711f5ed23eb7e6b1a
// TODO(pravk03): use the same file name and directory structure as kubelet for easier backports.

// CPUDetails is a map from CPU ID to Core ID, Socket ID, and NUMA ID.
type CPUDetails map[int]CPUInfo

func (d CPUDetails) KeepOnly(cpus cpuset.CPUSet) CPUDetails {
	result := CPUDetails{}
	for cpu, info := range d {
		if cpus.Contains(cpu) {
			result[cpu] = info
		}
	}
	return result
}

// CPUs returns all of the logical CPU IDs in this CPUDetails.
func (d CPUDetails) CPUs() cpuset.CPUSet {
	var cpuIDs []int
	for cpuID := range d {
		cpuIDs = append(cpuIDs, cpuID)
	}
	return cpuset.New(cpuIDs...)
}

// CPUsInNUMANodes returns all of the logical CPU IDs associated with the given
// NUMANode IDs in this CPUDetails.
func (d CPUDetails) CPUsInNUMANodes(ids ...int) cpuset.CPUSet {
	var cpuIDs []int
	for _, id := range ids {
		for cpu, info := range d {
			if info.NUMANodeID == id {
				cpuIDs = append(cpuIDs, cpu)
			}
		}
	}
	return cpuset.New(cpuIDs...)
}

// CPUsInCores returns all of the logical CPU IDs associated with the given
// core IDs in this CPUDetails.
func (d CPUDetails) CPUsInCores(ids ...int) cpuset.CPUSet {
	var cpuIDs []int
	for _, id := range ids {
		for cpu, info := range d {
			if info.CoreID == id {
				cpuIDs = append(cpuIDs, cpu)
			}
		}
	}
	return cpuset.New(cpuIDs...)
}

// CPUsInSockets returns all of the logical CPU IDs associated with the given
// socket IDs in this CPUDetails.
func (d CPUDetails) CPUsInSockets(ids ...int) cpuset.CPUSet {
	var cpuIDs []int
	for _, id := range ids {
		for cpu, info := range d {
			if info.SocketID == id {
				cpuIDs = append(cpuIDs, cpu)
			}
		}
	}
	return cpuset.New(cpuIDs...)
}

// CPUsInUncoreCaches returns all the logical CPU IDs associated with the given
// UnCoreCache IDs in this CPUDetails
func (d CPUDetails) CPUsInUncoreCaches(ids ...int) cpuset.CPUSet {
	var cpuIDs []int
	for _, id := range ids {
		for cpu, info := range d {
			if info.UncoreCacheID == id {
				cpuIDs = append(cpuIDs, cpu)
			}
		}
	}
	return cpuset.New(cpuIDs...)
}

// NUMANodes returns all of the NUMANode IDs associated with the CPUs in this
// CPUDetails.
func (d CPUDetails) NUMANodes() cpuset.CPUSet {
	var numaNodeIDs []int
	for _, info := range d {
		numaNodeIDs = append(numaNodeIDs, info.NUMANodeID)
	}
	return cpuset.New(numaNodeIDs...)
}

// Sockets returns all of the socket IDs associated with the CPUs in this
// CPUDetails.
func (d CPUDetails) Sockets() cpuset.CPUSet {
	var socketIDs []int
	for _, info := range d {
		socketIDs = append(socketIDs, info.SocketID)
	}
	return cpuset.New(socketIDs...)
}

// UnCoresInNUMANodes returns all of the uncore IDs associated with the given
// NUMANode IDs in this CPUDetails.
func (d CPUDetails) UncoreInNUMANodes(ids ...int) cpuset.CPUSet {
	var unCoreIDs []int
	for _, id := range ids {
		for _, info := range d {
			if info.NUMANodeID == id {
				unCoreIDs = append(unCoreIDs, info.UncoreCacheID)
			}
		}
	}
	return cpuset.New(unCoreIDs...)
}

// SocketsInNUMANodes returns all of the logical Socket IDs associated with the
// given NUMANode IDs in this CPUDetails.
func (d CPUDetails) SocketsInNUMANodes(ids ...int) cpuset.CPUSet {
	var socketIDs []int
	for _, id := range ids {
		for _, info := range d {
			if info.NUMANodeID == id {
				socketIDs = append(socketIDs, info.SocketID)
			}
		}
	}
	return cpuset.New(socketIDs...)
}

// CoresInSockets returns all of the core IDs associated with the given socket
// IDs in this CPUDetails.
func (d CPUDetails) CoresInSockets(ids ...int) cpuset.CPUSet {
	var coreIDs []int
	for _, id := range ids {
		for _, info := range d {
			if info.SocketID == id {
				coreIDs = append(coreIDs, info.CoreID)
			}
		}
	}
	return cpuset.New(coreIDs...)
}

// NUMANodesInSockets returns all of the logical NUMANode IDs associated with
// the given socket IDs in this CPUDetails.
func (d CPUDetails) NUMANodesInSockets(ids ...int) cpuset.CPUSet {
	var numaNodeIDs []int
	for _, id := range ids {
		for _, info := range d {
			if info.SocketID == id {
				numaNodeIDs = append(numaNodeIDs, info.NUMANodeID)
			}
		}
	}
	return cpuset.New(numaNodeIDs...)
}

// CoresInNUMANodes returns all of the core IDs associated with the given
// NUMANode IDs in this CPUDetails.
func (d CPUDetails) CoresInNUMANodes(ids ...int) cpuset.CPUSet {
	var coreIDs []int
	for _, id := range ids {
		for _, info := range d {
			if info.NUMANodeID == id {
				coreIDs = append(coreIDs, info.CoreID)
			}
		}
	}
	return cpuset.New(coreIDs...)
}

// CoresNeededInUncoreCache returns either the full list of all available unique core IDs associated with the given
// UnCoreCache IDs in this CPUDetails or subset that matches the ask.
func (d CPUDetails) CoresNeededInUncoreCache(numCoresNeeded int, ids ...int) cpuset.CPUSet {
	coreIDs := d.coresInUncoreCache(ids...)
	if coreIDs.Size() <= numCoresNeeded {
		return coreIDs
	}
	tmpCoreIDs := coreIDs.List()
	return cpuset.New(tmpCoreIDs[:numCoresNeeded]...)
}

// Helper function that just gets the cores
func (d CPUDetails) coresInUncoreCache(ids ...int) cpuset.CPUSet {
	var coreIDs []int
	for _, id := range ids {
		for _, info := range d {
			if info.UncoreCacheID == id {
				coreIDs = append(coreIDs, info.CoreID)
			}
		}
	}
	return cpuset.New(coreIDs...)
}
