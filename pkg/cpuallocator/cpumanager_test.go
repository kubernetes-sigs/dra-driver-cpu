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

	allocator := NewCPUManager(testDriverName, &topo)
	logger := testr.New(t)

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
