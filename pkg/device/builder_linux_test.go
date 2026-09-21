//go:build linux

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

package device_test

import (
	"testing"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/cpuset"
)

// TestBuildRejectsNegativeNUMANodeAttribute checks that the validation owned by
// the upstream deviceattribute helpers is actually wired in: a device with no
// NUMA affinity must not publish the standard numaNode attribute, because a
// negative value would match every other NUMA-less device.
//
// Topology discovery already rejects CPUs without a NUMA node before devices
// are built, so this exercises the guard directly rather than through a
// reachable production path.
func TestBuildRejectsNegativeNUMANodeAttribute(t *testing.T) {
	topo := &cpuinfo.CPUTopology{
		NumCPUs: 1,
		CPUDetails: cpuinfo.CPUDetails{
			0: {CpuID: 0, CoreID: 0, SocketID: 0, NUMANodeID: -1, SiblingCPUID: -1},
		},
	}

	_, err := device.Build(device.BuildInput{
		Inventory: device.Inventory{
			CPUTopology:  topo,
			ReservedCPUs: cpuset.New(),
		},
		Layout:         device.LayoutIndividual,
		PCIeRootMapper: store.NewPCIeRootMapper(),
	})
	require.ErrorContains(t, err, "numaNode")
}
