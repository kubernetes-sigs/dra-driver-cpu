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
	"fmt"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

const (
	DefaultDriverName           = "dra.cpu"
	CPUResourceQualifiedNameKey = resourceapi.QualifiedName("dra.cpu/cpu")
)

// ValidateResourceClaim validates dra.cpu-specific rules on a ResourceClaim.
// It returns a list of human-readable error strings.
func ValidateResourceClaim(claim *resourceapi.ResourceClaim, driverName string) []string {
	if claim == nil {
		return []string{"resourceClaim is nil"}
	}

	var errs []string
	for _, request := range claim.Spec.Devices.Requests {
		if request.Exactly != nil && request.Exactly.DeviceClassName == driverName {
			errs = append(errs, validateExactDeviceRequest(request.Name, request.Exactly, driverName)...)
		}

		if len(request.FirstAvailable) == 0 {
			continue
		}
		for _, subRequest := range request.FirstAvailable {
			if subRequest.DeviceClassName == driverName {
				errs = append(errs, fmt.Sprintf("request %q: firstAvailable is not supported for %s", request.Name, driverName))
				break
			}
		}
	}

	return errs
}

func validateExactDeviceRequest(requestName string, req *resourceapi.ExactDeviceRequest, driverName string) []string {
	var errs []string

	if req.DeviceClassName != driverName {
		return errs
	}

	if req.AdminAccess != nil && *req.AdminAccess {
		errs = append(errs, fmt.Sprintf("request %q: adminAccess is not supported for %s", requestName, driverName))
	}

	if req.AllocationMode != "" && req.AllocationMode != resourceapi.DeviceAllocationModeExactCount {
		errs = append(errs, fmt.Sprintf("request %q: allocationMode %q is not supported for %s", requestName, req.AllocationMode, driverName))
	}

	count := req.Count
	if count == 0 {
		count = 1
	}
	if count < 1 {
		errs = append(errs, fmt.Sprintf("request %q: count must be greater than zero for %s", requestName, driverName))
	}

	if req.Capacity == nil || len(req.Capacity.Requests) == 0 {
		return errs
	}

	if count != 1 {
		errs = append(errs, fmt.Sprintf("request %q: capacity requests require count 1 for %s", requestName, driverName))
	}

	if len(req.Capacity.Requests) != 1 {
		errs = append(errs, fmt.Sprintf("request %q: capacity requests must only include %q for %s", requestName, CPUResourceQualifiedNameKey, driverName))
		return errs
	}

	quantity, ok := req.Capacity.Requests[CPUResourceQualifiedNameKey]
	if !ok {
		errs = append(errs, fmt.Sprintf("request %q: capacity requests must include %q for %s", requestName, CPUResourceQualifiedNameKey, driverName))
		return errs
	}

	if !isPositiveIntegerQuantity(quantity) {
		errs = append(errs, fmt.Sprintf("request %q: capacity %q must be a positive integer for %s", requestName, CPUResourceQualifiedNameKey, driverName))
	}

	return errs
}

func isPositiveIntegerQuantity(quantity resource.Quantity) bool {
	value, ok := quantity.AsInt64()
	if !ok {
		return false
	}
	if value < 1 {
		return false
	}
	intQuantity := resource.NewQuantity(value, quantity.Format)
	return quantity.Cmp(*intQuantity) == 0
}
