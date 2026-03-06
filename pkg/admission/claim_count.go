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
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/util/sets"
)

// ExactRequestCPUCount returns the CPU count for a single ExactDeviceRequest.
// Prefers consumable capacity when present; otherwise uses Count.
func ExactRequestCPUCount(req *resourceapi.ExactDeviceRequest) int64 {
	if req == nil {
		return 0
	}
	count := req.Count
	if count < 1 {
		count = 1
	}
	if req.Capacity != nil && len(req.Capacity.Requests) > 0 {
		quantity, ok := req.Capacity.Requests[CPUResourceQualifiedNameKey]
		if !ok {
			return 0
		}
		value, ok := quantity.AsInt64()
		if !ok || value < 1 {
			return 0
		}
		if value*1000 != quantity.MilliValue() {
			return 0
		}
		return value * count
	}
	return count
}

// ClaimCPUCountFromSpec returns the total CPU count from a claim's Spec.Devices.Requests
// for the given driver (ExactDeviceRequest only).
func ClaimCPUCountFromSpec(claim *resourceapi.ResourceClaim, driverName string) int64 {
	if claim == nil {
		return 0
	}
	var total int64
	for _, request := range claim.Spec.Devices.Requests {
		if request.Exactly == nil || request.Exactly.DeviceClassName != driverName {
			continue
		}
		total += ExactRequestCPUCount(request.Exactly)
	}
	return total
}

// ClaimCPUCountFromAllocation returns the total CPU count from a claim's allocation
// by looking up device capacities in the given ResourceSlices (e.g. from List).
func ClaimCPUCountFromAllocation(claim *resourceapi.ResourceClaim, driverName string, slices []resourceapi.ResourceSlice) int64 {
	if claim == nil || claim.Status.Allocation == nil || len(claim.Status.Allocation.Devices.Results) == 0 {
		return 0
	}
	deviceNames := sets.New[string]()
	for _, result := range claim.Status.Allocation.Devices.Results {
		if result.Driver != driverName || result.Device == "" {
			continue
		}
		deviceNames.Insert(result.Device)
	}
	if deviceNames.Len() == 0 {
		return 0
	}
	deviceCapacities := make(map[string]map[resourceapi.QualifiedName]resourceapi.DeviceCapacity)
	for _, slice := range slices {
		for _, device := range slice.Spec.Devices {
			deviceCapacities[device.Name] = device.Capacity
		}
	}
	var total int64
	for deviceName := range deviceNames {
		capacities, ok := deviceCapacities[deviceName]
		if !ok {
			continue
		}
		if capacity, ok := capacities[CPUResourceQualifiedNameKey]; ok {
			total += capacity.Value.Value()
			continue
		}
		total++
	}
	return total
}
