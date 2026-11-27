/*
Copyright The Kubernetes Authors.

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
	"github.com/go-logr/logr"
)

func DisambiguateTopology(logger logr.Logger, topo *CPUTopology) *CPUTopology {
	type coreKey struct {
		socket, cluster, core int
	}

	result := *topo
	result.CPUDetails = make(CPUDetails, len(topo.CPUDetails))
	coreIDs := make(map[coreKey]int)

	// Like kubelet's getUniqueCoreID, use the lowest CPU ID on each core.
	for _, cpu := range topo.CPUDetails.CPUs().List() {
		info := topo.CPUDetails[cpu]
		key := coreKey{info.SocketID, info.ClusterID, info.CoreID}
		id, found := coreIDs[key]
		if !found {
			id = cpu
			coreIDs[key] = id
			logger.V(4).Info("mapped physical core to lowest CPU ID for allocator",
				"socketID", info.SocketID, "clusterID", info.ClusterID,
				"coreID", info.CoreID, "allocatorCoreID", id)
		}
		info.CoreID = id
		result.CPUDetails[cpu] = info
	}
	return &result
}
