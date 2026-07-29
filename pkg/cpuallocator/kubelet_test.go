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

func TestKubeletGetPreferredCPUs(t *testing.T) {
	allocator := NewKubelet(testDriverName, testCPUTopology())
	logger := testr.New(t)

	testCases := []struct {
		name       string
		allocation *resourceapi.AllocationResult
		alloc      resourceapi.DeviceRequestAllocationResult
		want       cpuset.CPUSet
		wantErr    error
	}{
		{
			name:    "nil allocation returns empty preferred set",
			want:    cpuset.New(),
			wantErr: nil,
		},
		{
			name: "config for different request is ignored",
			allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Config: []resourceapi.DeviceAllocationConfiguration{
						testOpaqueConfig(testDriverName, []string{"other-request"}),
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
						testOpaqueConfig("other-driver", []string{"claim-1"}),
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
						testOpaqueConfig(testDriverName, []string{"claim-1"}),
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

func TestKubeletAllocate(t *testing.T) {
	logger := testr.New(t)
	testCases := []struct {
		name          string
		availableCPUs cpuset.CPUSet
		preferredCPUs cpuset.CPUSet
		count         int
		want          cpuset.CPUSet
	}{
		{
			name:          "uses preferred CPUs when enough are available",
			availableCPUs: cpuset.New(0, 1, 2, 3),
			preferredCPUs: cpuset.New(0, 2),
			count:         2,
			want:          cpuset.New(0, 2),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			allocator := NewKubelet(testDriverName, testCPUTopology())

			got, err := allocator.Allocate(logger, tc.availableCPUs, tc.preferredCPUs, tc.count)
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func testOpaqueConfig(driverName string, requests []string) resourceapi.DeviceAllocationConfiguration {
	return resourceapi.DeviceAllocationConfiguration{
		Source:   resourceapi.AllocationConfigSourceClaim,
		Requests: requests,
		DeviceConfiguration: resourceapi.DeviceConfiguration{
			Opaque: &resourceapi.OpaqueDeviceConfiguration{
				Driver: driverName,
				Parameters: runtime.RawExtension{
					Raw: []byte(`{"apiVersion":"v1alpha1","cpuConfig":{"cpuset":"0,2"}}`),
				},
			},
		},
	}
}

func testCPUTopology() *cpuinfo.CPUTopology {
	return &cpuinfo.CPUTopology{
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
}
