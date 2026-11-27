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

package cpuallocator

import (
	"maps"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/cpuset"
)

const testDriverName = "dra.cpu"

func TestCPUManagerGetPreferredCPUs(t *testing.T) {
	topo := cpuinfo.CPUTopology{
		NumCPUs:      4,
		NumSockets:   1,
		NumNUMANodes: 1,
		NumCores:     2,
		CPUDetails: map[int]cpuinfo.CPUInfo{
			0: {CoreID: 0, SocketID: 0, NUMANodeID: 0},
			1: {CoreID: 1, SocketID: 0, NUMANodeID: 0},
			2: {CoreID: 0, SocketID: 0, NUMANodeID: 0},
			3: {CoreID: 1, SocketID: 0, NUMANodeID: 0},
		},
	}

	logger := testr.New(t)
	allocator := NewCPUManager(testDriverName, cpuinfo.DisambiguateTopology(logger, &topo))

	testCases := []struct {
		name       string
		allocation *resourceapi.AllocationResult
		alloc      resourceapi.DeviceRequestAllocationResult
		want       cpuset.CPUSet
		wantErr    error
	}{
		{
			name: "config for different request is ignored",
			allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Config: []resourceapi.DeviceAllocationConfiguration{
						{
							Source:   resourceapi.AllocationConfigSourceClaim,
							Requests: []string{"other-request"},
							DeviceConfiguration: resourceapi.DeviceConfiguration{
								Opaque: &resourceapi.OpaqueDeviceConfiguration{
									Driver: testDriverName,
									Parameters: runtime.RawExtension{
										Raw: []byte(`{"apiVersion":"v1alpha1","cpuConfig":{"cpuset":"0,2"}}`),
									},
								},
							},
						},
					},
				},
			},
			alloc: resourceapi.DeviceRequestAllocationResult{
				Request: "claim-1",
			},
			want:    cpuset.New(),
			wantErr: nil,
		},
		{
			name: "config for different driver is ignored",
			allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Config: []resourceapi.DeviceAllocationConfiguration{
						{
							Source:   resourceapi.AllocationConfigSourceClaim,
							Requests: []string{"claim-1"},
							DeviceConfiguration: resourceapi.DeviceConfiguration{
								Opaque: &resourceapi.OpaqueDeviceConfiguration{
									Driver: "other-driver",
									Parameters: runtime.RawExtension{
										Raw: []byte(`{"apiVersion":"v1alpha1","cpuConfig":{"cpuset":"0,2"}}`),
									},
								},
							},
						},
					},
				},
			},
			alloc: resourceapi.DeviceRequestAllocationResult{
				Request: "claim-1",
			},
			want:    cpuset.New(),
			wantErr: nil,
		},
		{
			name: "matching opaque config is rejected",
			allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Config: []resourceapi.DeviceAllocationConfiguration{
						{
							Source:   resourceapi.AllocationConfigSourceClaim,
							Requests: []string{"claim-1"},
							DeviceConfiguration: resourceapi.DeviceConfiguration{
								Opaque: &resourceapi.OpaqueDeviceConfiguration{
									Driver: testDriverName,
									Parameters: runtime.RawExtension{
										Raw: []byte(`{"apiVersion":"v1alpha1","cpuConfig":{"cpuset":"0,2"}}`),
									},
								},
							},
						},
					},
				},
			},
			alloc: resourceapi.DeviceRequestAllocationResult{
				Request: "claim-1",
			},
			want:    cpuset.New(),
			wantErr: ErrUnsupportedPreferredCPUs,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := allocator.GetPreferredCPUs(logger, tc.allocation, tc.alloc)
			require.Equal(t, tc.want, got)
			if tc.wantErr == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tc.wantErr)
		})
	}
}

func TestCPUManagerAllocateWholeCores(t *testing.T) {
	socketTopology := cpuinfo.CPUTopology{
		NumCPUs: 16, NumCores: 8, NumSockets: 2, NumNUMANodes: 2,
		NumUncoreCache: 2, SMTEnabled: true,
		CPUDetails: cpuinfo.CPUDetails{
			0:  {CoreID: 0, SocketID: 0, ClusterID: -1, NUMANodeID: 0, UncoreCacheID: 0},
			1:  {CoreID: 1, SocketID: 0, ClusterID: -1, NUMANodeID: 0, UncoreCacheID: 0},
			2:  {CoreID: 2, SocketID: 0, ClusterID: -1, NUMANodeID: 0, UncoreCacheID: 0},
			3:  {CoreID: 3, SocketID: 0, ClusterID: -1, NUMANodeID: 0, UncoreCacheID: 0},
			4:  {CoreID: 0, SocketID: 1, ClusterID: -1, NUMANodeID: 1, UncoreCacheID: 1},
			5:  {CoreID: 1, SocketID: 1, ClusterID: -1, NUMANodeID: 1, UncoreCacheID: 1},
			6:  {CoreID: 2, SocketID: 1, ClusterID: -1, NUMANodeID: 1, UncoreCacheID: 1},
			7:  {CoreID: 3, SocketID: 1, ClusterID: -1, NUMANodeID: 1, UncoreCacheID: 1},
			8:  {CoreID: 0, SocketID: 0, ClusterID: -1, NUMANodeID: 0, UncoreCacheID: 0},
			9:  {CoreID: 1, SocketID: 0, ClusterID: -1, NUMANodeID: 0, UncoreCacheID: 0},
			10: {CoreID: 2, SocketID: 0, ClusterID: -1, NUMANodeID: 0, UncoreCacheID: 0},
			11: {CoreID: 3, SocketID: 0, ClusterID: -1, NUMANodeID: 0, UncoreCacheID: 0},
			12: {CoreID: 0, SocketID: 1, ClusterID: -1, NUMANodeID: 1, UncoreCacheID: 1},
			13: {CoreID: 1, SocketID: 1, ClusterID: -1, NUMANodeID: 1, UncoreCacheID: 1},
			14: {CoreID: 2, SocketID: 1, ClusterID: -1, NUMANodeID: 1, UncoreCacheID: 1},
			15: {CoreID: 3, SocketID: 1, ClusterID: -1, NUMANodeID: 1, UncoreCacheID: 1},
		},
	}
	clusterTopology := socketTopology
	clusterTopology.NumSockets = 1
	clusterTopology.CPUDetails = maps.Clone(socketTopology.CPUDetails)
	for cpu, info := range clusterTopology.CPUDetails {
		info.ClusterID = info.SocketID
		info.SocketID = 0
		clusterTopology.CPUDetails[cpu] = info
	}

	testCases := []struct {
		name      string
		topology  cpuinfo.CPUTopology
		available cpuset.CPUSet
		want      cpuset.CPUSet
	}{
		{
			name:      "repeated core IDs across sockets, first NUMA node",
			topology:  socketTopology,
			available: cpuset.New(1, 2, 3, 8, 9, 10, 11),
			want:      cpuset.New(1, 9),
		},
		{
			name:      "repeated core IDs across sockets, second NUMA node",
			topology:  socketTopology,
			available: cpuset.New(5, 6, 7, 12, 13, 14, 15),
			want:      cpuset.New(5, 13),
		},
		{
			name:      "repeated core IDs across clusters",
			topology:  clusterTopology,
			available: cpuset.New(1, 2, 3, 8, 9, 10, 11),
			want:      cpuset.New(1, 9),
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			original := tc.topology
			original.CPUDetails = maps.Clone(tc.topology.CPUDetails)
			logger := testr.New(t)
			allocator := NewCPUManager(testDriverName, cpuinfo.DisambiguateTopology(logger, &tc.topology))
			got, err := allocator.Allocate(logger, tc.available, cpuset.New(), 2)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
			require.Equal(t, original, tc.topology, "allocator must preserve the discovered topology")
		})
	}
}
