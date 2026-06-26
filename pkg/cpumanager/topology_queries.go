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

package cpumanager

import (
	topology "github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"k8s.io/utils/cpuset"
)

// topologyQueries keeps exact topology queries in one place while preserving
// the distinction between the full topology and the accumulator's currently
// available CPUs.
type topologyQueries struct {
	topology      *topology.CPUTopology
	availableCPUs *topology.CPUDetails
}

func newTopologyQueries(topo *topology.CPUTopology, availableCPUs *topology.CPUDetails) topologyQueries {
	return topologyQueries{
		topology:      topo,
		availableCPUs: availableCPUs,
	}
}

func (q topologyQueries) allCPUs() cpuset.CPUSet {
	return q.topology.CPUDetails.CPUs()
}

func (q topologyQueries) availableCPUsSet() cpuset.CPUSet {
	return q.availableCPUs.CPUs()
}

func (q topologyQueries) allNUMANodes() cpuset.CPUSet {
	return q.topology.CPUDetails.NUMANodes()
}

func (q topologyQueries) availableNUMANodes() cpuset.CPUSet {
	return q.availableCPUs.NUMANodes()
}

func (q topologyQueries) availableSockets() cpuset.CPUSet {
	return q.availableCPUs.Sockets()
}

func (q topologyQueries) allCPUsInNUMANodes(ids ...int) cpuset.CPUSet {
	return q.topology.CPUDetails.CPUsInNUMANodes(ids...)
}

func (q topologyQueries) availableCPUsInNUMANodes(ids ...int) cpuset.CPUSet {
	return q.availableCPUs.CPUsInNUMANodes(ids...)
}

func (q topologyQueries) allCPUsInSockets(ids ...int) cpuset.CPUSet {
	return q.topology.CPUDetails.CPUsInSockets(ids...)
}

func (q topologyQueries) availableCPUsInSockets(ids ...int) cpuset.CPUSet {
	return q.availableCPUs.CPUsInSockets(ids...)
}

func (q topologyQueries) allCPUsInUncoreCaches(ids ...int) cpuset.CPUSet {
	return q.topology.CPUDetails.CPUsInUncoreCaches(ids...)
}

func (q topologyQueries) availableCPUsInUncoreCaches(ids ...int) cpuset.CPUSet {
	return q.availableCPUs.CPUsInUncoreCaches(ids...)
}

func (q topologyQueries) allCPUsInCoreKeys(keys ...topology.CoreKey) cpuset.CPUSet {
	return q.topology.CPUDetails.CPUsInCoreKeys(keys...)
}

func (q topologyQueries) availableCPUsInCoreKeys(keys ...topology.CoreKey) cpuset.CPUSet {
	return q.availableCPUs.CPUsInCoreKeys(keys...)
}

func (q topologyQueries) availableUncoreInNUMANodes(ids ...int) cpuset.CPUSet {
	return q.availableCPUs.UncoreInNUMANodes(ids...)
}

func (q topologyQueries) availableSocketsInNUMANodes(ids ...int) cpuset.CPUSet {
	return q.availableCPUs.SocketsInNUMANodes(ids...)
}

func (q topologyQueries) availableNUMANodesInSockets(ids ...int) cpuset.CPUSet {
	return q.availableCPUs.NUMANodesInSockets(ids...)
}

func (q topologyQueries) availableCoreKeysInSockets(ids ...int) []topology.CoreKey {
	return q.availableCPUs.CoreKeysInSockets(ids...)
}

func (q topologyQueries) availableCoreKeysInNUMANodes(ids ...int) []topology.CoreKey {
	return q.availableCPUs.CoreKeysInNUMANodes(ids...)
}

func (q topologyQueries) availableCoreKeysNeededForCPUsInUncoreCache(numCPUsNeeded int, ids ...int) []topology.CoreKey {
	return q.availableCPUs.CoreKeysNeededForCPUsInUncoreCache(numCPUsNeeded, ids...)
}
