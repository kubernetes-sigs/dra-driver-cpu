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
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/cpuset"
)

func TestExternalAllocate(t *testing.T) {
	tests := []struct {
		name          string
		availableCPUs cpuset.CPUSet
		preferredCPUs cpuset.CPUSet
		count         int
		expectedCPUs  cpuset.CPUSet
		expectErr     bool
	}{
		{
			name:          "corner case: all empty",
			availableCPUs: cpuset.New(),
			// GetPreferredCPUs must catch this; Allocate() per se can pass
			preferredCPUs: cpuset.New(),
			count:         0,
			expectedCPUs:  cpuset.New(),
		},
		{
			name:          "happy path: preferred is subset of available",
			availableCPUs: cpuset.New(0, 1, 2, 3),
			preferredCPUs: cpuset.New(1, 2),
			count:         2,
			expectedCPUs:  cpuset.New(1, 2),
		},
		{
			name:          "happy path: preferred equals available",
			availableCPUs: cpuset.New(0, 1, 2, 3),
			preferredCPUs: cpuset.New(0, 1, 2, 3),
			count:         4,
			expectedCPUs:  cpuset.New(0, 1, 2, 3),
		},
		{
			name:          "happy path: no preferred",
			availableCPUs: cpuset.New(0, 1, 2, 3),
			// GetPreferredCPUs must catch this; Allocate() per se can pass
			preferredCPUs: cpuset.New(),
			count:         0,
			expectedCPUs:  cpuset.New(),
		},
		{
			name:          "error path: more preferred than available",
			availableCPUs: cpuset.New(0, 1, 2, 3),
			preferredCPUs: cpuset.New(0, 1, 2, 3, 4),
			count:         5,
			expectedCPUs:  cpuset.New(),
			expectErr:     true,
		},
		{
			name:          "error path: preferred not proper subset of available",
			availableCPUs: cpuset.New(0, 1),
			preferredCPUs: cpuset.New(1, 2),
			count:         2,
			expectErr:     true,
		},
		{
			name:          "error path: preferred disjoint from available",
			availableCPUs: cpuset.New(0, 1),
			preferredCPUs: cpuset.New(2, 3),
			count:         2,
			expectErr:     true,
		},
		{
			name:          "error path: preferred non-empty but available empty",
			availableCPUs: cpuset.New(),
			preferredCPUs: cpuset.New(0),
			count:         1,
			expectErr:     true,
		},
	}

	alc := NewExternal("test-driver", cpuset.CPUSet{}, cpuset.CPUSet{})
	logger := testr.New(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := alc.Allocate(logger, tt.availableCPUs, tt.preferredCPUs, tt.count)

			if tt.expectErr {
				if err == nil {
					t.Fatalf("Allocate(%v, %v, %d) = %v, expected error", tt.availableCPUs, tt.preferredCPUs, tt.count, got)
				}
				return
			}

			if err != nil {
				t.Fatalf("Allocate(%v, %v, %d) unexpected error: %v", tt.availableCPUs, tt.preferredCPUs, tt.count, err)
			}
			if !got.Equals(tt.expectedCPUs) {
				t.Errorf("Allocate(%v, %v, %d) = %v, expected %v", tt.availableCPUs, tt.preferredCPUs, tt.count, got, tt.expectedCPUs)
			}
		})
	}
}

func TestExternalAllocateIsIdempotent(t *testing.T) {
	alc := NewExternal("test-driver", cpuset.CPUSet{}, cpuset.CPUSet{})
	logger := testr.New(t)

	availableCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	preferredCPUs := cpuset.New(1, 2)
	expectedCPUs := cpuset.New(1, 2)
	count := 2

	// this is an approximation. We test "enough time" and we try to catch
	// obvious sign of non-idempotent test.

	maxAttempts := 100                 // random "high enough" number which doesn't take too much in CI
	for attempt := range maxAttempts { // random "high enough" number which doesn't take too much in CI
		got, err := alc.Allocate(logger, availableCPUs, preferredCPUs, count)
		if err != nil {
			t.Fatalf("unexpected failure at allocation attempt %d/%d", attempt, maxAttempts)
		}
		if !got.Equals(expectedCPUs) {
			t.Fatalf("unexpected allocated cpus %s wanted %s", got, expectedCPUs)
		}
	}
}

func TestExternalValidate(t *testing.T) {
	tests := []struct {
		name          string
		preferredCPUs cpuset.CPUSet
		preparedCPUs  cpuset.CPUSet
		assignedCPUs  cpuset.CPUSet
		expectErr     bool
	}{
		{
			name:          "corner case: all empty",
			preferredCPUs: cpuset.New(),
			preparedCPUs:  cpuset.New(),
			assignedCPUs:  cpuset.New(),
		},
		{
			name:          "happy: no overlap at all",
			preferredCPUs: cpuset.New(0, 1),
			preparedCPUs:  cpuset.New(4, 5),
			assignedCPUs:  cpuset.New(2, 3),
		},
		{
			// GetPreferredCPUs must catch this; Validate() per se can pass
			name:          "happy path: empty preferred",
			preferredCPUs: cpuset.New(),
			preparedCPUs:  cpuset.New(0, 1),
			assignedCPUs:  cpuset.New(2, 3),
		},
		{
			name:          "error path: overlap with CPUs already assigned in this same claim/batch",
			preferredCPUs: cpuset.New(0, 1),
			preparedCPUs:  cpuset.New(),
			assignedCPUs:  cpuset.New(1, 2),
			expectErr:     true,
		},
		{
			name:          "error path: overlap with CPUs already prepared for other active claims on the node",
			preferredCPUs: cpuset.New(0, 1),
			preparedCPUs:  cpuset.New(1, 2),
			assignedCPUs:  cpuset.New(),
			expectErr:     true,
		},
		{
			name:          "error path: overlap with both prepared and assigned CPUs",
			preferredCPUs: cpuset.New(0, 1, 2),
			preparedCPUs:  cpuset.New(2, 5),
			assignedCPUs:  cpuset.New(1, 6),
			expectErr:     true,
		},
	}

	alc := NewExternal("test-driver", cpuset.CPUSet{}, cpuset.CPUSet{})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := alc.Validate(tt.preferredCPUs, tt.preparedCPUs, tt.assignedCPUs)
			gotErr := (err != nil)

			if tt.expectErr && !gotErr {
				t.Fatalf("Validate(%v, %v, %v) = nil, expected error", tt.preferredCPUs, tt.preparedCPUs, tt.assignedCPUs)
			}
			if !tt.expectErr && gotErr {
				t.Fatalf("Validate(%v, %v, %v) unexpected error: %v", tt.preferredCPUs, tt.preparedCPUs, tt.assignedCPUs, err)
			}
		})
	}
}

func TestExternalGetPreferredCPUs(t *testing.T) {
	tests := []struct {
		name         string
		onlineCPUs   cpuset.CPUSet
		reservedCPUs cpuset.CPUSet
		allocation   *resourceapi.AllocationResult
		expectedCPUs cpuset.CPUSet
		expectErr    bool
	}{
		{
			name:       "error: no opaque config targets the request",
			onlineCPUs: cpuset.New(0, 1, 2, 3),
			allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Results: []resourceapi.DeviceRequestAllocationResult{
						{
							Request: "req",
							Driver:  "dra.cpu",
							Device:  "cpudev0",
							ConsumedCapacity: map[resourceapi.QualifiedName]resource.Quantity{
								resourceapi.QualifiedName(device.CPUResourceQualifiedName): *resource.NewQuantity(1, resource.DecimalSI),
							},
						},
					},
				},
			},
			expectErr: true,
		},
		{
			name:       "multi-result request: opaque size matches request total",
			onlineCPUs: cpuset.New(0, 1, 2, 3),
			allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Config: []resourceapi.DeviceAllocationConfiguration{
						{
							Source:   resourceapi.AllocationConfigSourceClaim,
							Requests: []string{"req"},
							DeviceConfiguration: resourceapi.DeviceConfiguration{
								Opaque: &resourceapi.OpaqueDeviceConfiguration{
									Driver:     "dra.cpu",
									Parameters: runtime.RawExtension{Raw: []byte(`{"apiVersion":"v1alpha1","cpuConfig":{"cpuset":"0,2"}}`)},
								},
							},
						},
					},
					Results: []resourceapi.DeviceRequestAllocationResult{
						{
							Request: "req",
							Driver:  "dra.cpu",
							Device:  "cpudev0",
							ConsumedCapacity: map[resourceapi.QualifiedName]resource.Quantity{
								resourceapi.QualifiedName(device.CPUResourceQualifiedName): *resource.NewQuantity(1, resource.DecimalSI),
							},
						},
						{
							Request: "req",
							Driver:  "dra.cpu",
							Device:  "cpudev1",
							ConsumedCapacity: map[resourceapi.QualifiedName]resource.Quantity{
								resourceapi.QualifiedName(device.CPUResourceQualifiedName): *resource.NewQuantity(1, resource.DecimalSI),
							},
						},
					},
				},
			},
			expectedCPUs: cpuset.New(0, 2),
		},
		{
			name:       "single-result request: opaque size matches the single result",
			onlineCPUs: cpuset.New(0, 1, 2, 3, 4, 5),
			allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Config: []resourceapi.DeviceAllocationConfiguration{
						{
							Source:   resourceapi.AllocationConfigSourceClaim,
							Requests: []string{"req"},
							DeviceConfiguration: resourceapi.DeviceConfiguration{
								Opaque: &resourceapi.OpaqueDeviceConfiguration{
									Driver:     "dra.cpu",
									Parameters: runtime.RawExtension{Raw: []byte(`{"apiVersion":"v1alpha1","cpuConfig":{"cpuset":"2-5"}}`)},
								},
							},
						},
					},
					Results: []resourceapi.DeviceRequestAllocationResult{
						{
							Request: "req",
							Driver:  "dra.cpu",
							Device:  "cpudevmachine",
							ConsumedCapacity: map[resourceapi.QualifiedName]resource.Quantity{
								resourceapi.QualifiedName(device.CPUResourceQualifiedName): *resource.NewQuantity(4, resource.DecimalSI),
							},
						},
					},
				},
			},
			expectedCPUs: cpuset.New(2, 3, 4, 5),
		},
		// should never happen because they would mean we had a bug elsewhere: this responses is illegal, so we use forged illegal objects to ensure the code robustness
		{
			name:       "error: opaque size larger than request total",
			onlineCPUs: cpuset.New(0, 1, 2, 3),
			allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Config: []resourceapi.DeviceAllocationConfiguration{
						{
							Source:   resourceapi.AllocationConfigSourceClaim,
							Requests: []string{"req"},
							DeviceConfiguration: resourceapi.DeviceConfiguration{
								Opaque: &resourceapi.OpaqueDeviceConfiguration{
									Driver:     "dra.cpu",
									Parameters: runtime.RawExtension{Raw: []byte(`{"apiVersion":"v1alpha1","cpuConfig":{"cpuset":"0,1,2"}}`)},
								},
							},
						},
					},
					Results: []resourceapi.DeviceRequestAllocationResult{
						{
							Request: "req",
							Driver:  "dra.cpu",
							Device:  "cpudev0",
							ConsumedCapacity: map[resourceapi.QualifiedName]resource.Quantity{
								resourceapi.QualifiedName(device.CPUResourceQualifiedName): *resource.NewQuantity(1, resource.DecimalSI),
							},
						},
						{
							Request: "req",
							Driver:  "dra.cpu",
							Device:  "cpudev1",
							ConsumedCapacity: map[resourceapi.QualifiedName]resource.Quantity{
								resourceapi.QualifiedName(device.CPUResourceQualifiedName): *resource.NewQuantity(1, resource.DecimalSI),
							},
						},
					},
				},
			},
			expectErr: true,
		},
		// should never happen because they would mean we had a bug elsewhere: this responses is illegal, so we use forged illegal objects to ensure the code robustness
		{
			name:       "forged: error: opaque size smaller than request total",
			onlineCPUs: cpuset.New(0, 1, 2, 3),
			allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Config: []resourceapi.DeviceAllocationConfiguration{
						{
							Source:   resourceapi.AllocationConfigSourceClaim,
							Requests: []string{"req"},
							DeviceConfiguration: resourceapi.DeviceConfiguration{
								Opaque: &resourceapi.OpaqueDeviceConfiguration{
									Driver:     "dra.cpu",
									Parameters: runtime.RawExtension{Raw: []byte(`{"apiVersion":"v1alpha1","cpuConfig":{"cpuset":"0"}}`)},
								},
							},
						},
					},
					Results: []resourceapi.DeviceRequestAllocationResult{
						{
							Request: "req",
							Driver:  "dra.cpu",
							Device:  "cpudev0",
							ConsumedCapacity: map[resourceapi.QualifiedName]resource.Quantity{
								resourceapi.QualifiedName(device.CPUResourceQualifiedName): *resource.NewQuantity(1, resource.DecimalSI),
							},
						},
						{
							Request: "req",
							Driver:  "dra.cpu",
							Device:  "cpudev1",
							ConsumedCapacity: map[resourceapi.QualifiedName]resource.Quantity{
								resourceapi.QualifiedName(device.CPUResourceQualifiedName): *resource.NewQuantity(1, resource.DecimalSI),
							},
						},
					},
				},
			},
			expectErr: true,
		},
		// should never happen: represent invalid driver configuration that somehow (extremely unlikely) was gone undetected and caused a malformed allocation
		{
			name:       "forged: error: opaque cpuset contains unknown cores",
			onlineCPUs: cpuset.New(0, 1, 2, 3),
			allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Config: []resourceapi.DeviceAllocationConfiguration{
						{
							Source:   resourceapi.AllocationConfigSourceClaim,
							Requests: []string{"req"},
							DeviceConfiguration: resourceapi.DeviceConfiguration{
								Opaque: &resourceapi.OpaqueDeviceConfiguration{
									Driver:     "dra.cpu",
									Parameters: runtime.RawExtension{Raw: []byte(`{"apiVersion":"v1alpha1","cpuConfig":{"cpuset":"0,9"}}`)},
								},
							},
						},
					},
					Results: []resourceapi.DeviceRequestAllocationResult{
						{
							Request: "req",
							Driver:  "dra.cpu",
							Device:  "cpudev0",
							ConsumedCapacity: map[resourceapi.QualifiedName]resource.Quantity{
								resourceapi.QualifiedName(device.CPUResourceQualifiedName): *resource.NewQuantity(1, resource.DecimalSI),
							},
						},
						{
							Request: "req",
							Driver:  "dra.cpu",
							Device:  "cpudev1",
							ConsumedCapacity: map[resourceapi.QualifiedName]resource.Quantity{
								resourceapi.QualifiedName(device.CPUResourceQualifiedName): *resource.NewQuantity(1, resource.DecimalSI),
							},
						},
					},
				},
			},
			expectErr: true,
		},
		// should never happen: represent invalid driver configuration that somehow (extremely unlikely) was gone undetected and caused a malformed allocation
		{
			name:         "forged: error: opaque cpuset contains reserved cores",
			onlineCPUs:   cpuset.New(0, 1, 2, 3),
			reservedCPUs: cpuset.New(1),
			allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Config: []resourceapi.DeviceAllocationConfiguration{
						{
							Source:   resourceapi.AllocationConfigSourceClaim,
							Requests: []string{"req"},
							DeviceConfiguration: resourceapi.DeviceConfiguration{
								Opaque: &resourceapi.OpaqueDeviceConfiguration{
									Driver:     "dra.cpu",
									Parameters: runtime.RawExtension{Raw: []byte(`{"apiVersion":"v1alpha1","cpuConfig":{"cpuset":"0,1"}}`)},
								},
							},
						},
					},
					Results: []resourceapi.DeviceRequestAllocationResult{
						{
							Request: "req",
							Driver:  "dra.cpu",
							Device:  "cpudev0",
							ConsumedCapacity: map[resourceapi.QualifiedName]resource.Quantity{
								resourceapi.QualifiedName(device.CPUResourceQualifiedName): *resource.NewQuantity(1, resource.DecimalSI),
							},
						},
						{
							Request: "req",
							Driver:  "dra.cpu",
							Device:  "cpudev1",
							ConsumedCapacity: map[resourceapi.QualifiedName]resource.Quantity{
								resourceapi.QualifiedName(device.CPUResourceQualifiedName): *resource.NewQuantity(1, resource.DecimalSI),
							},
						},
					},
				},
			},
			expectErr: true,
		},
		{
			name:       "misconfiguration: error: request targeted by more than one opaque config",
			onlineCPUs: cpuset.New(0, 1, 2, 3),
			allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Config: []resourceapi.DeviceAllocationConfiguration{
						{
							Source:   resourceapi.AllocationConfigSourceClaim,
							Requests: []string{"req"},
							DeviceConfiguration: resourceapi.DeviceConfiguration{
								Opaque: &resourceapi.OpaqueDeviceConfiguration{
									Driver:     "dra.cpu",
									Parameters: runtime.RawExtension{Raw: []byte(`{"apiVersion":"v1alpha1","cpuConfig":{"cpuset":"0"}}`)},
								},
							},
						},
						{
							Source:   resourceapi.AllocationConfigSourceClaim,
							Requests: []string{"req"},
							DeviceConfiguration: resourceapi.DeviceConfiguration{
								Opaque: &resourceapi.OpaqueDeviceConfiguration{
									Driver:     "dra.cpu",
									Parameters: runtime.RawExtension{Raw: []byte(`{"apiVersion":"v1alpha1","cpuConfig":{"cpuset":"1"}}`)},
								},
							},
						},
					},
					Results: []resourceapi.DeviceRequestAllocationResult{
						{
							Request: "req",
							Driver:  "dra.cpu",
							Device:  "cpudev0",
							ConsumedCapacity: map[resourceapi.QualifiedName]resource.Quantity{
								resourceapi.QualifiedName(device.CPUResourceQualifiedName): *resource.NewQuantity(1, resource.DecimalSI),
							},
						},
					},
				},
			},
			expectErr: true,
		},
	}

	logger := testr.New(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alc := NewExternal("dra.cpu", tt.onlineCPUs, tt.reservedCPUs)

			for _, alloc := range tt.allocation.Devices.Results {
				got, err := alc.GetPreferredCPUs(logger, tt.allocation, alloc)

				if tt.expectErr {
					if err == nil {
						t.Fatalf("GetPreferredCPUs() = %v, expected error", got)
					}
					return
				}
				if err != nil {
					t.Fatalf("GetPreferredCPUs() unexpected error: %v", err)
				}
				if !got.Equals(tt.expectedCPUs) {
					t.Errorf("GetPreferredCPUs() = %v, expected %v", got, tt.expectedCPUs)
				}
			}
		})
	}
}
