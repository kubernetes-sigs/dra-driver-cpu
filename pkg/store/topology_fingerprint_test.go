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

package store

import (
	"testing"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/cpuset"
)

func newFingerprintTestTopology() cpuinfo.CPUTopology {
	topology := cpuinfo.CPUTopology{
		NumCPUs:        4,
		NumCores:       2,
		NumUncoreCache: 2,
		NumSockets:     1,
		NumNUMANodes:   2,
		SMTEnabled:     true,
	}
	topology.CPUDetails = map[int]cpuinfo.CPUInfo{
		0: {
			CpuID:          0,
			CoreID:         0,
			SocketID:       0,
			ClusterID:      0,
			NUMANodeID:     0,
			NumaNodeCPUSet: cpuset.New(0, 1),
			SiblingCPUID:   1,
			CoreType:       cpuinfo.CoreTypePerformance,
			UncoreCacheID:  0,
		},
		1: {
			CpuID:          1,
			CoreID:         0,
			SocketID:       0,
			ClusterID:      0,
			NUMANodeID:     0,
			NumaNodeCPUSet: cpuset.New(0, 1),
			SiblingCPUID:   0,
			CoreType:       cpuinfo.CoreTypePerformance,
			UncoreCacheID:  0,
		},
		2: {
			CpuID:          2,
			CoreID:         1,
			SocketID:       0,
			ClusterID:      1,
			NUMANodeID:     1,
			NumaNodeCPUSet: cpuset.New(2, 3),
			SiblingCPUID:   3,
			CoreType:       cpuinfo.CoreTypePerformance,
			UncoreCacheID:  1,
		},
		3: {
			CpuID:          3,
			CoreID:         1,
			SocketID:       0,
			ClusterID:      1,
			NUMANodeID:     1,
			NumaNodeCPUSet: cpuset.New(2, 3),
			SiblingCPUID:   2,
			CoreType:       cpuinfo.CoreTypePerformance,
			UncoreCacheID:  1,
		},
	}
	return topology
}

func TestMakeTopologyFingerprintCompatibility(t *testing.T) {
	// Literal fingerprints pin the preamble and digest independently of the encoder under test.
	for _, tc := range []struct {
		name     string
		topology cpuinfo.CPUTopology
		want     string
	}{
		{
			// hash value computed offline
			name: "empty",
			want: "tpfpv001\x00\x00\x00\x00\x00\x00\x00\x00" + "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		},
		{
			// hash value computed offline
			name:     "baseline",
			topology: newFingerprintTestTopology(),
			want:     "tpfpv001\x00\x00\x00\x00\x00\x00\x00\x00" + "ea1e578bdf31d9a91cdafca1c3f8af7145a191aa9a02a1918da965723a9a21e5",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, MakeTopologyFingerprint(tc.topology))
		})
	}
}

func TestMakeTopologyFingerprintOrderIndependence(t *testing.T) {
	// regression test. Iterate over "many" identical maps
	// to get random iterations order, which we don't control,
	// and check we always get the same fingerprint.
	// This is meant to be a first line of defense against
	// order-sensitive fingerprinting, but can't be
	// an exhaustive test
	reference := MakeTopologyFingerprint(newFingerprintTestTopology())
	for range 100 { // 100 is a random "high enough" number
		require.Equal(t, reference, MakeTopologyFingerprint(newFingerprintTestTopology()))
	}
}

func TestMakeTopologyFingerprintFieldChanges(t *testing.T) {
	baseline := MakeTopologyFingerprint(newFingerprintTestTopology())
	require.NotEmpty(t, baseline)

	for _, tc := range []struct {
		name           string
		mutateCPU      func(*cpuinfo.CPUInfo)
		mutateTopology func(*cpuinfo.CPUTopology)
		unchanged      bool
	}{
		{
			name: "CPU added",
			mutateTopology: func(topology *cpuinfo.CPUTopology) {
				topology.CPUDetails[4] = cpuinfo.CPUInfo{CpuID: 4}
			},
		},
		{
			name: "CPU removed",
			mutateTopology: func(topology *cpuinfo.CPUTopology) {
				delete(topology.CPUDetails, 0)
			},
		},
		{
			name: "CPUDetails key",
			mutateTopology: func(topology *cpuinfo.CPUTopology) {
				topology.CPUDetails[4] = topology.CPUDetails[0]
				delete(topology.CPUDetails, 0)
			},
		},
		{
			name:      "CpuID",
			unchanged: true,
			mutateCPU: func(info *cpuinfo.CPUInfo) { info.CpuID = 4 },
		},
		{
			name:      "CoreID",
			mutateCPU: func(info *cpuinfo.CPUInfo) { info.CoreID = 2 },
		},
		{
			name:      "SocketID",
			mutateCPU: func(info *cpuinfo.CPUInfo) { info.SocketID = 1 },
		},
		{
			name:      "ClusterID",
			mutateCPU: func(info *cpuinfo.CPUInfo) { info.ClusterID = 2 },
		},
		{
			name:      "NUMANodeID",
			mutateCPU: func(info *cpuinfo.CPUInfo) { info.NUMANodeID = 1 },
		},
		{
			name:      "NumaNodeCPUSet",
			unchanged: true,
			mutateCPU: func(info *cpuinfo.CPUInfo) { info.NumaNodeCPUSet = cpuset.New(0, 2) },
		},
		{
			name:      "SiblingCPUID",
			unchanged: true,
			mutateCPU: func(info *cpuinfo.CPUInfo) { info.SiblingCPUID = 2 },
		},
		{
			name:      "CoreType",
			unchanged: true,
			mutateCPU: func(info *cpuinfo.CPUInfo) { info.CoreType = cpuinfo.CoreTypeEfficiency },
		},
		{
			name:      "UncoreCacheID",
			mutateCPU: func(info *cpuinfo.CPUInfo) { info.UncoreCacheID = 1 },
		},
		{
			name:           "NumCPUs",
			unchanged:      true,
			mutateTopology: func(topology *cpuinfo.CPUTopology) { topology.NumCPUs = 5 },
		},
		{
			name:           "NumCores",
			unchanged:      true,
			mutateTopology: func(topology *cpuinfo.CPUTopology) { topology.NumCores = 3 },
		},
		{
			name:           "NumUncoreCache",
			unchanged:      true,
			mutateTopology: func(topology *cpuinfo.CPUTopology) { topology.NumUncoreCache = 3 },
		},
		{
			name:           "NumSockets",
			unchanged:      true,
			mutateTopology: func(topology *cpuinfo.CPUTopology) { topology.NumSockets = 2 },
		},
		{
			name:           "NumNUMANodes",
			unchanged:      true,
			mutateTopology: func(topology *cpuinfo.CPUTopology) { topology.NumNUMANodes = 3 },
		},
		{
			name:           "SMTEnabled",
			unchanged:      true,
			mutateTopology: func(topology *cpuinfo.CPUTopology) { topology.SMTEnabled = false },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			topology := newFingerprintTestTopology()
			if tc.mutateCPU != nil {
				info := topology.CPUDetails[0]
				tc.mutateCPU(&info)
				topology.CPUDetails[0] = info
			} else {
				tc.mutateTopology(&topology)
			}
			if tc.unchanged {
				require.Equal(t, baseline, MakeTopologyFingerprint(topology))
			} else {
				require.NotEqual(t, baseline, MakeTopologyFingerprint(topology))
			}
		})
	}
}
