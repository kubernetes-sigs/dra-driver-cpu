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
	"fmt"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/cpuset"
)

func TestAllocate(t *testing.T) {
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
		// Selector semantics (Option B): a single opaque cpuset can back several
		// allocation results of the same request. Allocate must therefore take
		// exactly `count` CPUs out of the preferred hint (restricted to what is
		// still available for this result), rather than returning the whole hint.
		// The choice is deterministic (lowest-numbered first) so that repeated
		// Prepare calls are idempotent; the specific CPUs picked per result are
		// irrelevant because the driver applies the union across the claim.
		{
			name:          "selector: take lowest count when preferred exceeds count",
			availableCPUs: cpuset.New(0, 1, 2, 3),
			preferredCPUs: cpuset.New(0, 1, 2, 3),
			count:         2,
			expectedCPUs:  cpuset.New(0, 1),
		},
		{
			name:          "selector: multi-result split, first result draws one CPU",
			availableCPUs: cpuset.New(0, 1, 2, 3),
			preferredCPUs: cpuset.New(0, 2),
			count:         1,
			expectedCPUs:  cpuset.New(0),
		},
		{
			name: "selector: multi-result split, second result draws from what remains",
			// models availableCPUs already shrunk by the first result's pick {0}
			availableCPUs: cpuset.New(1, 2, 3),
			preferredCPUs: cpuset.New(0, 2),
			count:         1,
			expectedCPUs:  cpuset.New(2),
		},
		{
			name:          "selector: preferred straddles available, take count from the intersection",
			availableCPUs: cpuset.New(2, 3, 4, 5),
			preferredCPUs: cpuset.New(0, 1, 2, 3),
			count:         2,
			expectedCPUs:  cpuset.New(2, 3),
		},
	}

	alc := NewExternal("test-driver", nil, cpuset.CPUSet{}, cpuset.CPUSet{})
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

func TestValidate(t *testing.T) {
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

	alc := NewExternal("test-driver", nil, cpuset.CPUSet{}, cpuset.CPUSet{})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := alc.Validate(tt.preferredCPUs, tt.preparedCPUs, tt.assignedCPUs)

			if tt.expectErr && err == nil {
				t.Fatalf("Validate(%v, %v, %v) = nil, expected error", tt.preferredCPUs, tt.preparedCPUs, tt.assignedCPUs)
			}
			if !tt.expectErr && err != nil {
				t.Fatalf("Validate(%v, %v, %v) unexpected error: %v", tt.preferredCPUs, tt.preparedCPUs, tt.assignedCPUs, err)
			}
		})
	}
}

const testDriver = "dra.cpu"

// claimOpaqueConfig builds a per-claim opaque cpuset config targeting the given
// request(s). Passing no requests leaves the config applicable to all requests.
func claimOpaqueConfig(driver, cpuSet string, requests ...string) resourceapi.DeviceAllocationConfiguration {
	raw := fmt.Sprintf(`{"apiVersion":"v1alpha1","cpuConfig":{"cpuset":%q}}`, cpuSet)
	return resourceapi.DeviceAllocationConfiguration{
		Source:   resourceapi.AllocationConfigSourceClaim,
		Requests: requests,
		DeviceConfiguration: resourceapi.DeviceConfiguration{
			Opaque: &resourceapi.OpaqueDeviceConfiguration{
				Driver:     driver,
				Parameters: runtime.RawExtension{Raw: []byte(raw)},
			},
		},
	}
}

// cpuResult builds one allocation result consuming `cpus` units of CPU capacity.
// A request with count>1 expands into several of these sharing the same request.
func cpuResult(driver, request, deviceName string, cpus int64) resourceapi.DeviceRequestAllocationResult {
	return resourceapi.DeviceRequestAllocationResult{
		Request: request,
		Driver:  driver,
		Device:  deviceName,
		ConsumedCapacity: map[resourceapi.QualifiedName]resource.Quantity{
			resourceapi.QualifiedName(device.CPUResourceQualifiedName): *resource.NewQuantity(cpus, resource.DecimalSI),
		},
	}
}

func allocationWith(configs []resourceapi.DeviceAllocationConfiguration, results []resourceapi.DeviceRequestAllocationResult) *resourceapi.AllocationResult {
	return &resourceapi.AllocationResult{
		Devices: resourceapi.DeviceAllocationResult{
			Config:  configs,
			Results: results,
		},
	}
}

func TestGetPreferredCPUs(t *testing.T) {
	tests := []struct {
		name         string
		onlineCPUs   cpuset.CPUSet
		reservedCPUs cpuset.CPUSet
		allocation   *resourceapi.AllocationResult
		// allocIndex selects which result of the allocation is passed as the
		// per-result `alloc`, mirroring the driver's per-result Prepare loop.
		allocIndex   int
		expectedCPUs cpuset.CPUSet
		expectErr    bool
	}{
		{
			name:       "error: nil allocation",
			onlineCPUs: cpuset.New(0, 1, 2, 3),
			allocation: nil,
			expectErr:  true,
		},
		{
			name:       "error: no opaque config targets the request",
			onlineCPUs: cpuset.New(0, 1, 2, 3),
			allocation: allocationWith(
				nil,
				[]resourceapi.DeviceRequestAllocationResult{
					cpuResult(testDriver, "req", "cpudev0", 1),
				},
			),
			expectErr: true,
		},
		{
			// The exact scenario from PR #229: a request with count:2 (two
			// results, each consuming 1 CPU) plus one opaque cpuset of size 2.
			// The whole request's opaque hint is returned; the per-result count
			// of 1 must NOT cause a size-mismatch rejection.
			name:       "multi-result request: opaque size matches request total",
			onlineCPUs: cpuset.New(0, 1, 2, 3),
			allocation: allocationWith(
				[]resourceapi.DeviceAllocationConfiguration{
					claimOpaqueConfig(testDriver, "0,2", "req"),
				},
				[]resourceapi.DeviceRequestAllocationResult{
					cpuResult(testDriver, "req", "cpudev0", 1),
					cpuResult(testDriver, "req", "cpudev1", 1),
				},
			),
			allocIndex:   0,
			expectedCPUs: cpuset.New(0, 2),
		},
		{
			name:       "single-result request: opaque size matches the sole result",
			onlineCPUs: cpuset.New(0, 1, 2, 3, 4, 5),
			allocation: allocationWith(
				[]resourceapi.DeviceAllocationConfiguration{
					claimOpaqueConfig(testDriver, "2-5", "req"),
				},
				[]resourceapi.DeviceRequestAllocationResult{
					cpuResult(testDriver, "req", "cpudevmachine", 4),
				},
			),
			expectedCPUs: cpuset.New(2, 3, 4, 5),
		},
		{
			name:       "error: opaque size larger than request total",
			onlineCPUs: cpuset.New(0, 1, 2, 3),
			allocation: allocationWith(
				[]resourceapi.DeviceAllocationConfiguration{
					claimOpaqueConfig(testDriver, "0,1,2", "req"),
				},
				[]resourceapi.DeviceRequestAllocationResult{
					cpuResult(testDriver, "req", "cpudev0", 1),
					cpuResult(testDriver, "req", "cpudev1", 1),
				},
			),
			expectErr: true,
		},
		{
			name:       "error: opaque size smaller than request total",
			onlineCPUs: cpuset.New(0, 1, 2, 3),
			allocation: allocationWith(
				[]resourceapi.DeviceAllocationConfiguration{
					claimOpaqueConfig(testDriver, "0", "req"),
				},
				[]resourceapi.DeviceRequestAllocationResult{
					cpuResult(testDriver, "req", "cpudev0", 1),
					cpuResult(testDriver, "req", "cpudev1", 1),
				},
			),
			expectErr: true,
		},
		{
			name:       "error: opaque cpuset contains offline cores",
			onlineCPUs: cpuset.New(0, 1, 2, 3),
			allocation: allocationWith(
				[]resourceapi.DeviceAllocationConfiguration{
					claimOpaqueConfig(testDriver, "0,9", "req"),
				},
				[]resourceapi.DeviceRequestAllocationResult{
					cpuResult(testDriver, "req", "cpudev0", 1),
					cpuResult(testDriver, "req", "cpudev1", 1),
				},
			),
			expectErr: true,
		},
		{
			name:         "error: opaque cpuset contains reserved cores",
			onlineCPUs:   cpuset.New(0, 1, 2, 3),
			reservedCPUs: cpuset.New(1),
			allocation: allocationWith(
				[]resourceapi.DeviceAllocationConfiguration{
					claimOpaqueConfig(testDriver, "0,1", "req"),
				},
				[]resourceapi.DeviceRequestAllocationResult{
					cpuResult(testDriver, "req", "cpudev0", 1),
					cpuResult(testDriver, "req", "cpudev1", 1),
				},
			),
			expectErr: true,
		},
		{
			name:       "error: opaque config sourced from DeviceClass",
			onlineCPUs: cpuset.New(0, 1, 2, 3),
			allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Config: []resourceapi.DeviceAllocationConfiguration{
						{
							Source:   resourceapi.AllocationConfigSourceClass,
							Requests: []string{"req"},
							DeviceConfiguration: resourceapi.DeviceConfiguration{
								Opaque: &resourceapi.OpaqueDeviceConfiguration{
									Driver:     testDriver,
									Parameters: runtime.RawExtension{Raw: []byte(`{"apiVersion":"v1alpha1","cpuConfig":{"cpuset":"0"}}`)},
								},
							},
						},
					},
					Results: []resourceapi.DeviceRequestAllocationResult{
						cpuResult(testDriver, "req", "cpudev0", 1),
					},
				},
			},
			expectErr: true,
		},
		{
			name:       "error: request targeted by more than one opaque config",
			onlineCPUs: cpuset.New(0, 1, 2, 3),
			allocation: allocationWith(
				[]resourceapi.DeviceAllocationConfiguration{
					claimOpaqueConfig(testDriver, "0", "req"),
					claimOpaqueConfig(testDriver, "1", "req"),
				},
				[]resourceapi.DeviceRequestAllocationResult{
					cpuResult(testDriver, "req", "cpudev0", 1),
				},
			),
			expectErr: true,
		},
	}

	logger := testr.New(t)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			alc := NewExternal(testDriver, nil, tt.onlineCPUs, tt.reservedCPUs)

			var alloc resourceapi.DeviceRequestAllocationResult
			if tt.allocation != nil && len(tt.allocation.Devices.Results) > 0 {
				alloc = tt.allocation.Devices.Results[tt.allocIndex]
			}

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
		})
	}
}
