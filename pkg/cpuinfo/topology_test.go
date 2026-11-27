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
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/require"
)

func TestDisambiguateTopology(t *testing.T) {
	// Fixtures adapted from Test_Discover in Kubernetes:
	// https://github.com/kubernetes/kubernetes/blob/975a786f79cce5a29202e767dd597615bf514e04/pkg/kubelet/cm/cpumanager/topology/topology_test.go
	// k8s case: "DualSocketHT - non unique Core'ID's".
	dualSocketSMT := CPUTopology{
		NumCPUs: 12, NumCores: 6, NumSockets: 2, NumNUMANodes: 2,
		NumUncoreCache: 1, SMTEnabled: true,
		CPUDetails: CPUDetails{
			0:  {CpuID: 0, CoreID: 0, SocketID: 0, ClusterID: -1, NUMANodeID: 0, SiblingCPUID: 6},
			1:  {CpuID: 1, CoreID: 1, SocketID: 0, ClusterID: -1, NUMANodeID: 0, SiblingCPUID: 7},
			2:  {CpuID: 2, CoreID: 2, SocketID: 0, ClusterID: -1, NUMANodeID: 0, SiblingCPUID: 8},
			3:  {CpuID: 3, CoreID: 0, SocketID: 1, ClusterID: -1, NUMANodeID: 1, SiblingCPUID: 9},
			4:  {CpuID: 4, CoreID: 1, SocketID: 1, ClusterID: -1, NUMANodeID: 1, SiblingCPUID: 10},
			5:  {CpuID: 5, CoreID: 2, SocketID: 1, ClusterID: -1, NUMANodeID: 1, SiblingCPUID: 11},
			6:  {CpuID: 6, CoreID: 0, SocketID: 0, ClusterID: -1, NUMANodeID: 0, SiblingCPUID: 0},
			7:  {CpuID: 7, CoreID: 1, SocketID: 0, ClusterID: -1, NUMANodeID: 0, SiblingCPUID: 1},
			8:  {CpuID: 8, CoreID: 2, SocketID: 0, ClusterID: -1, NUMANodeID: 0, SiblingCPUID: 2},
			9:  {CpuID: 9, CoreID: 0, SocketID: 1, ClusterID: -1, NUMANodeID: 1, SiblingCPUID: 3},
			10: {CpuID: 10, CoreID: 1, SocketID: 1, ClusterID: -1, NUMANodeID: 1, SiblingCPUID: 4},
			11: {CpuID: 11, CoreID: 2, SocketID: 1, ClusterID: -1, NUMANodeID: 1, SiblingCPUID: 5},
		},
	}
	// Extend the same k8s case to repeated core IDs across clusters:
	// replace its two sockets with two clusters within one socket.
	clusterSMT := dualSocketSMT.Clone()
	clusterSMT.NumSockets = 1
	for cpu, info := range clusterSMT.CPUDetails {
		info.ClusterID = info.SocketID
		info.SocketID = 0
		clusterSMT.CPUDetails[cpu] = info
	}

	testCases := []struct {
		name        string
		topology    CPUTopology
		wantCoreIDs map[int]int
	}{
		{
			// k8s case: "OneSocketHT".
			name: "single socket SMT",
			topology: CPUTopology{
				NumCPUs: 8, NumCores: 4, NumSockets: 1, NumNUMANodes: 1,
				NumUncoreCache: 1, SMTEnabled: true,
				CPUDetails: CPUDetails{
					0: {CpuID: 0, CoreID: 0, ClusterID: -1, SiblingCPUID: 4},
					1: {CpuID: 1, CoreID: 1, ClusterID: -1, SiblingCPUID: 5},
					2: {CpuID: 2, CoreID: 2, ClusterID: -1, SiblingCPUID: 6},
					3: {CpuID: 3, CoreID: 3, ClusterID: -1, SiblingCPUID: 7},
					4: {CpuID: 4, CoreID: 0, ClusterID: -1, SiblingCPUID: 0},
					5: {CpuID: 5, CoreID: 1, ClusterID: -1, SiblingCPUID: 1},
					6: {CpuID: 6, CoreID: 2, ClusterID: -1, SiblingCPUID: 2},
					7: {CpuID: 7, CoreID: 3, ClusterID: -1, SiblingCPUID: 3},
				},
			},
			wantCoreIDs: map[int]int{0: 0, 1: 1, 2: 2, 3: 3, 4: 0, 5: 1, 6: 2, 7: 3},
		},
		{
			// k8s case: "DualSocketHT - non unique Core'ID's".
			name:     "repeated core IDs across sockets",
			topology: dualSocketSMT,
			wantCoreIDs: map[int]int{
				0: 0, 1: 1, 2: 2, 3: 3, 4: 4, 5: 5,
				6: 0, 7: 1, 8: 2, 9: 3, 10: 4, 11: 5,
			},
		},
		{
			// Cluster extension of the k8s dual-socket fixture above.
			name:     "repeated core IDs across clusters in one socket",
			topology: clusterSMT,
			wantCoreIDs: map[int]int{
				0: 0, 1: 1, 2: 2, 3: 3, 4: 4, 5: 5,
				6: 0, 7: 1, 8: 2, 9: 3, 10: 4, 11: 5,
			},
		},
		{
			// k8s case: "DualSocketNoHT".
			name: "dual socket SMT disabled",
			topology: CPUTopology{
				NumCPUs: 4, NumCores: 4, NumSockets: 2, NumNUMANodes: 2,
				NumUncoreCache: 1,
				CPUDetails: CPUDetails{
					0: {CpuID: 0, CoreID: 0, SocketID: 0, ClusterID: -1, NUMANodeID: 0, SiblingCPUID: -1},
					1: {CpuID: 1, CoreID: 1, SocketID: 1, ClusterID: -1, NUMANodeID: 1, SiblingCPUID: -1},
					2: {CpuID: 2, CoreID: 2, SocketID: 0, ClusterID: -1, NUMANodeID: 0, SiblingCPUID: -1},
					3: {CpuID: 3, CoreID: 3, SocketID: 1, ClusterID: -1, NUMANodeID: 1, SiblingCPUID: -1},
				},
			},
			wantCoreIDs: map[int]int{0: 0, 1: 1, 2: 2, 3: 3},
		},
		{
			name:        "empty topology",
			topology:    CPUTopology{CPUDetails: CPUDetails{}},
			wantCoreIDs: map[int]int{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			original := tc.topology.Clone()
			want := original.Clone()

			for cpu, id := range tc.wantCoreIDs {
				info := want.CPUDetails[cpu]
				info.CoreID = id
				want.CPUDetails[cpu] = info
			}

			got := DisambiguateTopology(testr.New(t), &tc.topology)
			require.NotSame(t, &tc.topology, got)
			require.Equal(t, want, *got)
			require.Equal(t, original, tc.topology, "remapping must not modify the input")

			got.NumCPUs++
			got.CPUDetails[-1] = CPUInfo{CoreID: -1}
			require.Equal(t, original, tc.topology, "the result must not alias the input")
		})
	}
}
