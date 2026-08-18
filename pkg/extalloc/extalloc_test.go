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

package extalloc

import (
	"testing"

	"github.com/go-logr/logr/testr"
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
	}

	alc := NewAllocator("test-driver", nil, cpuset.CPUSet{}, cpuset.CPUSet{})
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

	alc := NewAllocator("test-driver", nil, cpuset.CPUSet{}, cpuset.CPUSet{})

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
