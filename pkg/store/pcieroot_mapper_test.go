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

package store

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
)

func TestGetPCIeRootsForCPU(t *testing.T) {
	// random addresses, no special meaning
	domA := device.PCIeDomain{RootName: "pci0000:00"}
	domB := device.PCIeDomain{RootName: "pci0000:17"}
	domC := device.PCIeDomain{RootName: "pci0000:3a"}

	tests := []struct {
		name           string
		mapper         *PCIeRootMapper
		cpuIDs         []int
		expectedResult []string
	}{
		{
			name:           "NewPCIeRootMapper returns nil",
			mapper:         NewPCIeRootMapper(),
			cpuIDs:         []int{0, 1, 2},
			expectedResult: nil,
		},
		{
			name:           "no cpuIDs returns nil",
			mapper:         NewPCIeRootMapper(),
			cpuIDs:         nil,
			expectedResult: nil,
		},
		{
			name: "cpu not in mapping returns empty",
			mapper: &PCIeRootMapper{
				pcieDomains:       []device.PCIeDomain{domA},
				cpuIDToPCIeDomain: map[int][]*device.PCIeDomain{},
			},
			cpuIDs:         []int{0, 1}, // any value is ok
			expectedResult: []string{},
		},
		{
			name: "single cpu single domain",
			mapper: &PCIeRootMapper{
				pcieDomains: []device.PCIeDomain{domA},
				cpuIDToPCIeDomain: map[int][]*device.PCIeDomain{
					0: {&domA},
				},
			},
			cpuIDs:         []int{0},
			expectedResult: []string{"pci0000:00"},
		},
		{
			name: "single cpu multiple domains",
			mapper: &PCIeRootMapper{
				pcieDomains: []device.PCIeDomain{domA, domB},
				cpuIDToPCIeDomain: map[int][]*device.PCIeDomain{
					0: {&domA, &domB},
				},
			},
			cpuIDs:         []int{0},
			expectedResult: []string{"pci0000:00", "pci0000:17"},
		},
		{
			name: "multiple cpus same domain, deduplicating",
			mapper: &PCIeRootMapper{
				pcieDomains: []device.PCIeDomain{domA},
				cpuIDToPCIeDomain: map[int][]*device.PCIeDomain{
					0: {&domA},
					1: {&domA},
				},
			},
			cpuIDs:         []int{0, 1},
			expectedResult: []string{"pci0000:00"},
		},
		{
			name: "multiple cpus different domains",
			mapper: &PCIeRootMapper{
				pcieDomains: []device.PCIeDomain{domA, domB, domC},
				cpuIDToPCIeDomain: map[int][]*device.PCIeDomain{
					0: {&domA},
					1: {&domB},
					2: {&domC},
				},
			},
			cpuIDs:         []int{0, 1, 2},
			expectedResult: []string{"pci0000:00", "pci0000:17", "pci0000:3a"},
		},
		{
			name: "mixed known and unknown cpus",
			mapper: &PCIeRootMapper{
				pcieDomains: []device.PCIeDomain{domA},
				cpuIDToPCIeDomain: map[int][]*device.PCIeDomain{
					0: {&domA},
				},
			},
			cpuIDs:         []int{0, 99},
			expectedResult: []string{"pci0000:00"},
		},
		{
			name: "no cpuIDs with populated mapper returns empty",
			mapper: &PCIeRootMapper{
				pcieDomains: []device.PCIeDomain{domA},
				cpuIDToPCIeDomain: map[int][]*device.PCIeDomain{
					0: {&domA},
				},
			},
			cpuIDs:         nil,
			expectedResult: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.mapper.GetPCIeRootsForCPU(tt.cpuIDs...)
			if diff := cmp.Diff(tt.expectedResult, got); diff != "" {
				t.Errorf("GetPCIeRootsForCPU(%v) mismatch:\n%s", tt.cpuIDs, diff)
			}
		})
	}
}
