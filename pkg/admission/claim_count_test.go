/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use it except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package admission

import (
	"testing"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestExactRequestCPUCount_Nil(t *testing.T) {
	if got := ExactRequestCPUCount(nil); got != 0 {
		t.Errorf("ExactRequestCPUCount(nil) = %d, want 0", got)
	}
}

func TestExactRequestCPUCount_CountOnly(t *testing.T) {
	req := &resourceapi.ExactDeviceRequest{DeviceClassName: DefaultDriverName, Count: 4}
	if got := ExactRequestCPUCount(req); got != 4 {
		t.Errorf("ExactRequestCPUCount(count=4) = %d, want 4", got)
	}
}

func TestExactRequestCPUCount_CountZeroTreatedAsOne(t *testing.T) {
	req := &resourceapi.ExactDeviceRequest{DeviceClassName: DefaultDriverName, Count: 0}
	if got := ExactRequestCPUCount(req); got != 1 {
		t.Errorf("ExactRequestCPUCount(count=0) = %d, want 1", got)
	}
}

func TestExactRequestCPUCount_CapacityInteger(t *testing.T) {
	req := &resourceapi.ExactDeviceRequest{
		DeviceClassName: DefaultDriverName,
		Count:           1,
		Capacity: &resourceapi.CapacityRequirements{
			Requests: map[resourceapi.QualifiedName]resource.Quantity{
				CPUResourceQualifiedNameKey: resource.MustParse("2"),
			},
		},
	}
	if got := ExactRequestCPUCount(req); got != 2 {
		t.Errorf("ExactRequestCPUCount(capacity 2, count 1) = %d, want 2", got)
	}
}

func TestExactRequestCPUCount_CapacityTimesCount(t *testing.T) {
	req := &resourceapi.ExactDeviceRequest{
		DeviceClassName: DefaultDriverName,
		Count:           3,
		Capacity: &resourceapi.CapacityRequirements{
			Requests: map[resourceapi.QualifiedName]resource.Quantity{
				CPUResourceQualifiedNameKey: resource.MustParse("2"),
			},
		},
	}
	if got := ExactRequestCPUCount(req); got != 6 {
		t.Errorf("ExactRequestCPUCount(capacity 2, count 3) = %d, want 6", got)
	}
}

func TestExactRequestCPUCount_CapacityFractionalReturnsZero(t *testing.T) {
	req := &resourceapi.ExactDeviceRequest{
		DeviceClassName: DefaultDriverName,
		Count:           1,
		Capacity: &resourceapi.CapacityRequirements{
			Requests: map[resourceapi.QualifiedName]resource.Quantity{
				CPUResourceQualifiedNameKey: resource.MustParse("2.5"),
			},
		},
	}
	if got := ExactRequestCPUCount(req); got != 0 {
		t.Errorf("ExactRequestCPUCount(fractional capacity) = %d, want 0", got)
	}
}

func TestExactRequestCPUCount_CapacityMissingCPUKeyReturnsZero(t *testing.T) {
	req := &resourceapi.ExactDeviceRequest{
		DeviceClassName: DefaultDriverName,
		Count:           1,
		Capacity: &resourceapi.CapacityRequirements{
			Requests: map[resourceapi.QualifiedName]resource.Quantity{
				resourceapi.QualifiedName("other/key"): resource.MustParse("2"),
			},
		},
	}
	if got := ExactRequestCPUCount(req); got != 0 {
		t.Errorf("ExactRequestCPUCount(no CPU key) = %d, want 0", got)
	}
}

func TestClaimCPUCountFromSpec_Nil(t *testing.T) {
	if got := ClaimCPUCountFromSpec(nil, DefaultDriverName); got != 0 {
		t.Errorf("ClaimCPUCountFromSpec(nil) = %d, want 0", got)
	}
}

func TestClaimCPUCountFromSpec_SingleRequest(t *testing.T) {
	claim := newResourceClaim("c", exactCountRequest("r", DefaultDriverName, 4))
	if got := ClaimCPUCountFromSpec(claim, DefaultDriverName); got != 4 {
		t.Errorf("ClaimCPUCountFromSpec(single 4) = %d, want 4", got)
	}
}

func TestClaimCPUCountFromSpec_OtherDriverIgnored(t *testing.T) {
	claim := newResourceClaim("c", exactCountRequest("r", "other-driver", 4))
	if got := ClaimCPUCountFromSpec(claim, DefaultDriverName); got != 0 {
		t.Errorf("ClaimCPUCountFromSpec(other driver) = %d, want 0", got)
	}
}

func TestClaimCPUCountFromSpec_MultipleRequestsSummed(t *testing.T) {
	claim := &resourceapi.ResourceClaim{
		Spec: resourceapi.ResourceClaimSpec{
			Devices: resourceapi.DeviceClaim{
				Requests: []resourceapi.DeviceRequest{
					{Name: "a", Exactly: &resourceapi.ExactDeviceRequest{DeviceClassName: DefaultDriverName, Count: 2}},
					{Name: "b", Exactly: &resourceapi.ExactDeviceRequest{DeviceClassName: DefaultDriverName, Count: 3}},
				},
			},
		},
	}
	if got := ClaimCPUCountFromSpec(claim, DefaultDriverName); got != 5 {
		t.Errorf("ClaimCPUCountFromSpec(2+3) = %d, want 5", got)
	}
}

func TestClaimCPUCountFromAllocation_NilClaim(t *testing.T) {
	if got := ClaimCPUCountFromAllocation(nil, DefaultDriverName, nil); got != 0 {
		t.Errorf("ClaimCPUCountFromAllocation(nil claim) = %d, want 0", got)
	}
}

func TestClaimCPUCountFromAllocation_NoAllocation(t *testing.T) {
	claim := &resourceapi.ResourceClaim{Spec: resourceapi.ResourceClaimSpec{}}
	if got := ClaimCPUCountFromAllocation(claim, DefaultDriverName, nil); got != 0 {
		t.Errorf("ClaimCPUCountFromAllocation(no allocation) = %d, want 0", got)
	}
}

func TestClaimCPUCountFromAllocation_FromSliceCapacity(t *testing.T) {
	claim := &resourceapi.ResourceClaim{
		Status: resourceapi.ResourceClaimStatus{
			Allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Results: []resourceapi.DeviceRequestAllocationResult{
						{Driver: DefaultDriverName, Device: "dev1", Pool: "node1"},
					},
				},
			},
		},
	}
	slices := []resourceapi.ResourceSlice{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "s1"},
			Spec: resourceapi.ResourceSliceSpec{
				Devices: []resourceapi.Device{
					{
						Name: "dev1",
						Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
							CPUResourceQualifiedNameKey: {Value: resource.MustParse("4")},
						},
					},
				},
			},
		},
	}
	if got := ClaimCPUCountFromAllocation(claim, DefaultDriverName, slices); got != 4 {
		t.Errorf("ClaimCPUCountFromAllocation(one device, capacity 4) = %d, want 4", got)
	}
}

func TestClaimCPUCountFromAllocation_OneCorePerDeviceWhenNoCapacity(t *testing.T) {
	claim := &resourceapi.ResourceClaim{
		Status: resourceapi.ResourceClaimStatus{
			Allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Results: []resourceapi.DeviceRequestAllocationResult{
						{Driver: DefaultDriverName, Device: "dev1", Pool: "node1"},
						{Driver: DefaultDriverName, Device: "dev2", Pool: "node1"},
					},
				},
			},
		},
	}
	slices := []resourceapi.ResourceSlice{
		{
			Spec: resourceapi.ResourceSliceSpec{
				Devices: []resourceapi.Device{
					{Name: "dev1", Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{}},
					{Name: "dev2", Capacity: map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{}},
				},
			},
		},
	}
	if got := ClaimCPUCountFromAllocation(claim, DefaultDriverName, slices); got != 2 {
		t.Errorf("ClaimCPUCountFromAllocation(two devices, no CPU capacity) = %d, want 2", got)
	}
}

func TestClaimCPUCountFromAllocation_OtherDriverIgnored(t *testing.T) {
	claim := &resourceapi.ResourceClaim{
		Status: resourceapi.ResourceClaimStatus{
			Allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Results: []resourceapi.DeviceRequestAllocationResult{
						{Driver: "other", Device: "dev1", Pool: "node1"},
					},
				},
			},
		},
	}
	if got := ClaimCPUCountFromAllocation(claim, DefaultDriverName, nil); got != 0 {
		t.Errorf("ClaimCPUCountFromAllocation(other driver) = %d, want 0", got)
	}
}
