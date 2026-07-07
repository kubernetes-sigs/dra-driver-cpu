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

package cpuallocator

import (
	"github.com/go-logr/logr"
	topology "github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpumanager"
	"k8s.io/utils/cpuset"
)

// CPUManager allocates CPUs using the same algorithms implemented in
// the kubelet cpumanager when using its static policy.
// See the code in `pkg/cpumanager` for details.
type CPUManager struct {
	topo *topology.CPUTopology
}

func NewCPUManager(topo *topology.CPUTopology) *CPUManager {
	return &CPUManager{
		topo: topo,
	}
}

func (alc *CPUManager) Allocate(logger logr.Logger, availableCPUs, preferredCPUs cpuset.CPUSet, count int) (cpuset.CPUSet, error) {
	return cpumanager.TakeByTopologyNUMAPacked(logger, alc.topo, availableCPUs, count, cpumanager.CPUSortingStrategyPacked, true)
}
