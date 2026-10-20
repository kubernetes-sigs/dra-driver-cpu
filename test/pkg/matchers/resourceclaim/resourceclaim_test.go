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

package resourceclaim

import (
	"testing"

	resourcev1 "k8s.io/api/resource/v1"
)

func TestReuseSameDeviceForRequests(t *testing.T) {
	result := func(request, driver, pool, device string) resourcev1.DeviceRequestAllocationResult {
		return resourcev1.DeviceRequestAllocationResult{
			Request: request, Driver: driver, Pool: pool, Device: device,
		}
	}
	first := result("request-0", "dra.cpu", "node", "numa-0")
	second := result("request-1", "dra.cpu", "node", "numa-0")
	for _, tc := range []struct {
		name    string
		results []resourcev1.DeviceRequestAllocationResult
		want    bool
	}{
		{"same device", []resourcev1.DeviceRequestAllocationResult{first, second}, true},
		{"reversed results", []resourcev1.DeviceRequestAllocationResult{second, first}, true},
		{"no results", nil, false},
		{"missing request", []resourcev1.DeviceRequestAllocationResult{first}, false},
		{"duplicate request", []resourcev1.DeviceRequestAllocationResult{first, first}, false},
		{"extra result", []resourcev1.DeviceRequestAllocationResult{first, second, second}, false},
		{"different request", []resourcev1.DeviceRequestAllocationResult{first, result("request-2", "dra.cpu", "node", "numa-0")}, false},
		{"different device", []resourcev1.DeviceRequestAllocationResult{first, result("request-1", "dra.cpu", "node", "numa-1")}, false},
		{"same name in another pool", []resourcev1.DeviceRequestAllocationResult{first, result("request-1", "dra.cpu", "other-node", "numa-0")}, false},
		{"same name from another driver", []resourcev1.DeviceRequestAllocationResult{first, result("request-1", "other.driver", "node", "numa-0")}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			claim := &resourcev1.ResourceClaim{
				Status: resourcev1.ResourceClaimStatus{
					Allocation: &resourcev1.AllocationResult{
						Devices: resourcev1.DeviceAllocationResult{Results: tc.results},
					},
				},
			}
			got, err := ReuseSameDeviceForRequests("request-0", "request-1").Match(claim)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("match = %v, want %v", got, tc.want)
			}
		})
	}
}
