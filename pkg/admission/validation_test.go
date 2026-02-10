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

package admission

import (
	"strings"
	"testing"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// TestValidateResourceClaim_ValidExactCount accepts a supported exact count.
func TestValidateResourceClaim_ValidExactCount(t *testing.T) {
	claim := newResourceClaim("claim-ok", exactCountRequest("req", DefaultDriverName, 4))
	errs := ValidateResourceClaim(claim, DefaultDriverName)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

// TestValidateResourceClaim_UnsupportedAllocationMode rejects allocation modes.
func TestValidateResourceClaim_UnsupportedAllocationMode(t *testing.T) {
	req := exactCountRequest("req", DefaultDriverName, 1)
	req.AllocationMode = resourceapi.DeviceAllocationModeAll
	claim := newResourceClaim("claim-mode", req)

	errs := ValidateResourceClaim(claim, DefaultDriverName)
	assertErrorsContain(t, errs, "allocationMode")
}

// TestValidateResourceClaim_CapacityRequiresCountOne blocks capacity with count > 1.
func TestValidateResourceClaim_CapacityRequiresCountOne(t *testing.T) {
	req := exactCountRequest("req", DefaultDriverName, 2)
	req.Capacity = &resourceapi.CapacityRequirements{
		Requests: map[resourceapi.QualifiedName]resource.Quantity{
			CPUResourceQualifiedNameKey: resource.MustParse("2"),
		},
	}
	claim := newResourceClaim("claim-capacity", req)

	errs := ValidateResourceClaim(claim, DefaultDriverName)
	assertErrorsContain(t, errs, "capacity requests require count 1")
}

// TestValidateResourceClaim_CapacityRequiresCPUResource enforces dra.cpu/cpu key.
func TestValidateResourceClaim_CapacityRequiresCPUResource(t *testing.T) {
	req := exactCountRequest("req", DefaultDriverName, 1)
	req.Capacity = &resourceapi.CapacityRequirements{
		Requests: map[resourceapi.QualifiedName]resource.Quantity{
			resourceapi.QualifiedName("other/resource"): resource.MustParse("2"),
		},
	}
	claim := newResourceClaim("claim-capacity", req)

	errs := ValidateResourceClaim(claim, DefaultDriverName)
	assertErrorsContain(t, errs, "capacity requests must include")
}

// TestValidateResourceClaim_CapacityRequiresInteger rejects fractional capacity.
func TestValidateResourceClaim_CapacityRequiresInteger(t *testing.T) {
	req := exactCountRequest("req", DefaultDriverName, 1)
	req.Capacity = &resourceapi.CapacityRequirements{
		Requests: map[resourceapi.QualifiedName]resource.Quantity{
			CPUResourceQualifiedNameKey: resource.MustParse("2.5"),
		},
	}
	claim := newResourceClaim("claim-capacity", req)

	errs := ValidateResourceClaim(claim, DefaultDriverName)
	assertErrorsContain(t, errs, "must be a positive integer")
}

// TestValidateResourceClaim_FirstAvailableNotSupported rejects firstAvailable.
func TestValidateResourceClaim_FirstAvailableNotSupported(t *testing.T) {
	claim := &resourceapi.ResourceClaim{
		Spec: resourceapi.ResourceClaimSpec{
			Devices: resourceapi.DeviceClaim{
				Requests: []resourceapi.DeviceRequest{
					{
						Name: "req",
						FirstAvailable: []resourceapi.DeviceSubRequest{
							{Name: "sub", DeviceClassName: DefaultDriverName},
						},
					},
				},
			},
		},
	}

	errs := ValidateResourceClaim(claim, DefaultDriverName)
	assertErrorsContain(t, errs, "firstAvailable is not supported")
}

// newResourceClaim builds a ResourceClaim with a single request.
func newResourceClaim(name string, req resourceapi.ExactDeviceRequest) *resourceapi.ResourceClaim {
	return &resourceapi.ResourceClaim{
		Spec: resourceapi.ResourceClaimSpec{
			Devices: resourceapi.DeviceClaim{
				Requests: []resourceapi.DeviceRequest{
					{
						Name:    name,
						Exactly: &req,
					},
				},
			},
		},
	}
}

// exactCountRequest builds an ExactDeviceRequest for a driver and count.
func exactCountRequest(name, driver string, count int64) resourceapi.ExactDeviceRequest {
	return resourceapi.ExactDeviceRequest{
		DeviceClassName: driver,
		Count:           count,
	}
}

// assertErrorsContain ensures an expected substring appears in validation errors.
func assertErrorsContain(t *testing.T, errs []string, substr string) {
	t.Helper()
	for _, err := range errs {
		if strings.Contains(err, substr) {
			return
		}
	}
	t.Fatalf("expected errors to contain %q, got %v", substr, errs)
}
