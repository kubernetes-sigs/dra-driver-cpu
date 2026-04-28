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

package driver

import (
	"strings"
	"testing"

	"github.com/containerd/nri/pkg/api"
	"k8s.io/utils/cpuset"
)

func TestClaimAllocations_Validate(t *testing.T) {
	tests := []struct {
		name          string
		pod           *api.PodSandbox
		container     *api.Container
		claims        ClaimAllocations
		expectError   bool
		errorContains string
	}{
		{
			name: "matching container request - 10 CPUs",
			pod: &api.PodSandbox{
				Name:      "test-pod",
				Namespace: "default",
			},
			container: &api.Container{
				Name: "test-container",
				Linux: &api.LinuxContainer{
					Resources: &api.LinuxResources{
						Cpu: &api.LinuxCPU{
							Shares: &api.OptionalUInt64{Value: 10240}, // 10 CPUs * 1024
						},
					},
				},
			},
			claims: ClaimAllocations{
				"claim-1": cpuset.New(0, 1, 2, 3, 4, 5, 6, 7, 8, 9), // 10 CPUs
			},
			expectError: false,
		},
		{
			name: "matching container request - 4 CPUs",
			pod: &api.PodSandbox{
				Name:      "test-pod",
				Namespace: "default",
			},
			container: &api.Container{
				Name: "test-container",
				Linux: &api.LinuxContainer{
					Resources: &api.LinuxResources{
						Cpu: &api.LinuxCPU{
							Shares: &api.OptionalUInt64{Value: 4096}, // 4 CPUs * 1024
						},
					},
				},
			},
			claims: ClaimAllocations{
				"claim-1": cpuset.New(0, 1, 2, 3), // 4 CPUs
			},
			expectError: false,
		},
		{
			name: "mismatched container request - request too low",
			pod: &api.PodSandbox{
				Name:      "test-pod",
				Namespace: "default",
			},
			container: &api.Container{
				Name: "test-container",
				Linux: &api.LinuxContainer{
					Resources: &api.LinuxResources{
						Cpu: &api.LinuxCPU{
							Shares: &api.OptionalUInt64{Value: 5120}, // 5 CPUs * 1024
						},
					},
				},
			},
			claims: ClaimAllocations{
				"claim-1": cpuset.New(0, 1, 2, 3, 4, 5, 6, 7, 8, 9), // 10 CPUs
			},
			expectError:   true,
			errorContains: "does not match claim allocation",
		},
		{
			name: "mismatched container request - request too high",
			pod: &api.PodSandbox{
				Name:      "test-pod",
				Namespace: "default",
			},
			container: &api.Container{
				Name: "test-container",
				Linux: &api.LinuxContainer{
					Resources: &api.LinuxResources{
						Cpu: &api.LinuxCPU{
							Shares: &api.OptionalUInt64{Value: 20480}, // 20 CPUs * 1024
						},
					},
				},
			},
			claims: ClaimAllocations{
				"claim-1": cpuset.New(0, 1, 2, 3, 4, 5, 6, 7, 8, 9), // 10 CPUs
			},
			expectError:   true,
			errorContains: "does not match claim allocation",
		},
		{
			name: "no container CPU request specified - should pass",
			pod: &api.PodSandbox{
				Name:      "test-pod",
				Namespace: "default",
			},
			container: &api.Container{
				Name: "test-container",
				Linux: &api.LinuxContainer{
					Resources: &api.LinuxResources{
						Cpu: &api.LinuxCPU{},
					},
				},
			},
			claims: ClaimAllocations{
				"claim-1": cpuset.New(0, 1, 2, 3, 4, 5, 6, 7, 8, 9), // 10 CPUs
			},
			expectError: false,
		},
		{
			name: "container with zero CPU shares - should pass",
			pod: &api.PodSandbox{
				Name:      "test-pod",
				Namespace: "default",
			},
			container: &api.Container{
				Name: "test-container",
				Linux: &api.LinuxContainer{
					Resources: &api.LinuxResources{
						Cpu: &api.LinuxCPU{
							Shares: &api.OptionalUInt64{Value: 0},
						},
					},
				},
			},
			claims: ClaimAllocations{
				"claim-1": cpuset.New(0, 1, 2, 3, 4, 5, 6, 7, 8, 9), // 10 CPUs
			},
			expectError: false,
		},
		{
			name: "best-effort container with minimum CPU shares - should pass",
			pod: &api.PodSandbox{
				Name:      "test-pod",
				Namespace: "default",
			},
			container: &api.Container{
				Name: "test-container",
				Linux: &api.LinuxContainer{
					Resources: &api.LinuxResources{
						Cpu: &api.LinuxCPU{
							Shares: &api.OptionalUInt64{Value: 2}, // Kubernetes minimum for best-effort
						},
					},
				},
			},
			claims: ClaimAllocations{
				"claim-1": cpuset.New(0), // 1 CPU
			},
			expectError: false,
		},
		{
			name: "multiple claims - matching total",
			pod: &api.PodSandbox{
				Name:      "test-pod",
				Namespace: "default",
			},
			container: &api.Container{
				Name: "test-container",
				Linux: &api.LinuxContainer{
					Resources: &api.LinuxResources{
						Cpu: &api.LinuxCPU{
							Shares: &api.OptionalUInt64{Value: 10240}, // 10 CPUs * 1024
						},
					},
				},
			},
			claims: ClaimAllocations{
				"claim-1": cpuset.New(0, 1, 2, 3),       // 4 CPUs
				"claim-2": cpuset.New(4, 5, 6, 7, 8, 9), // 6 CPUs
			},
			expectError: false,
		},
		{
			name: "multiple claims - mismatched total",
			pod: &api.PodSandbox{
				Name:      "test-pod",
				Namespace: "default",
			},
			container: &api.Container{
				Name: "test-container",
				Linux: &api.LinuxContainer{
					Resources: &api.LinuxResources{
						Cpu: &api.LinuxCPU{
							Shares: &api.OptionalUInt64{Value: 8192}, // 8 CPUs * 1024
						},
					},
				},
			},
			claims: ClaimAllocations{
				"claim-1": cpuset.New(0, 1, 2, 3),       // 4 CPUs
				"claim-2": cpuset.New(4, 5, 6, 7, 8, 9), // 6 CPUs (total = 10)
			},
			expectError:   true,
			errorContains: "does not match claim allocation",
		},
		{
			name: "container without Linux resources - should pass",
			pod: &api.PodSandbox{
				Name:      "test-pod",
				Namespace: "default",
			},
			container: &api.Container{
				Name: "test-container",
				// No Linux field
			},
			claims: ClaimAllocations{
				"claim-1": cpuset.New(0, 1, 2, 3, 4, 5, 6, 7, 8, 9), // 10 CPUs
			},
			expectError: false,
		},
		{
			name: "single CPU claim - matching",
			pod: &api.PodSandbox{
				Name:      "test-pod",
				Namespace: "default",
			},
			container: &api.Container{
				Name: "test-container",
				Linux: &api.LinuxContainer{
					Resources: &api.LinuxResources{
						Cpu: &api.LinuxCPU{
							Shares: &api.OptionalUInt64{Value: 1024}, // 1 CPU * 1024
						},
					},
				},
			},
			claims: ClaimAllocations{
				"claim-1": cpuset.New(0), // 1 CPU
			},
			expectError: false,
		},
		{
			name: "edge case - off by one CPU (request 3, claim 4)",
			pod: &api.PodSandbox{
				Name:      "test-pod",
				Namespace: "default",
			},
			container: &api.Container{
				Name: "test-container",
				Linux: &api.LinuxContainer{
					Resources: &api.LinuxResources{
						Cpu: &api.LinuxCPU{
							Shares: &api.OptionalUInt64{Value: 3072}, // 3 CPUs * 1024
						},
					},
				},
			},
			claims: ClaimAllocations{
				"claim-1": cpuset.New(0, 1, 2, 3), // 4 CPUs
			},
			expectError:   true,
			errorContains: "does not match claim allocation",
		},
		{
			name: "edge case - within tolerance (1.009 CPUs vs 1 CPU)",
			pod: &api.PodSandbox{
				Name:      "test-pod",
				Namespace: "default",
			},
			container: &api.Container{
				Name: "test-container",
				Linux: &api.LinuxContainer{
					Resources: &api.LinuxResources{
						Cpu: &api.LinuxCPU{
							Shares: &api.OptionalUInt64{Value: 1033}, // ~1.009 CPUs
						},
					},
				},
			},
			claims: ClaimAllocations{
				"claim-1": cpuset.New(0), // 1 CPU
			},
			expectError: false,
		},
		{
			name: "edge case - just outside tolerance (1.011 CPUs vs 1 CPU)",
			pod: &api.PodSandbox{
				Name:      "test-pod",
				Namespace: "default",
			},
			container: &api.Container{
				Name: "test-container",
				Linux: &api.LinuxContainer{
					Resources: &api.LinuxResources{
						Cpu: &api.LinuxCPU{
							Shares: &api.OptionalUInt64{Value: 1035}, // ~1.011 CPUs
						},
					},
				},
			},
			claims: ClaimAllocations{
				"claim-1": cpuset.New(0), // 1 CPU
			},
			expectError:   true,
			errorContains: "does not match claim allocation",
		},
		{
			name: "edge case - minimum shares + 1 (3 shares)",
			pod: &api.PodSandbox{
				Name:      "test-pod",
				Namespace: "default",
			},
			container: &api.Container{
				Name: "test-container",
				Linux: &api.LinuxContainer{
					Resources: &api.LinuxResources{
						Cpu: &api.LinuxCPU{
							Shares: &api.OptionalUInt64{Value: 3}, // Just above minimum
						},
					},
				},
			},
			claims: ClaimAllocations{
				"claim-1": cpuset.New(0), // 1 CPU
			},
			expectError:   true,
			errorContains: "does not match claim allocation",
		},
		{
			name: "edge case - large CPU count (100 CPUs)",
			pod: &api.PodSandbox{
				Name:      "test-pod",
				Namespace: "default",
			},
			container: &api.Container{
				Name: "test-container",
				Linux: &api.LinuxContainer{
					Resources: &api.LinuxResources{
						Cpu: &api.LinuxCPU{
							Shares: &api.OptionalUInt64{Value: 102400}, // 100 CPUs * 1024
						},
					},
				},
			},
			claims: ClaimAllocations{
				"claim-1": cpuset.New(0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19,
					20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32, 33, 34, 35, 36, 37, 38, 39,
					40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59,
					60, 61, 62, 63, 64, 65, 66, 67, 68, 69, 70, 71, 72, 73, 74, 75, 76, 77, 78, 79,
					80, 81, 82, 83, 84, 85, 86, 87, 88, 89, 90, 91, 92, 93, 94, 95, 96, 97, 98, 99), // 100 CPUs
			},
			expectError: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.claims.Validate(tc.pod, tc.container)

			if tc.expectError && err == nil {
				t.Errorf("expected error but got none")
			}

			if !tc.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if tc.expectError && err != nil {
				if !strings.Contains(err.Error(), tc.errorContains) {
					t.Errorf("error %q does not contain expected substring %q", err.Error(), tc.errorContains)
				}
			}
		})
	}
}

func TestClaimAllocations_TotalCPUs(t *testing.T) {
	tests := []struct {
		name     string
		claims   ClaimAllocations
		expected int
	}{
		{
			name: "single claim with 4 CPUs",
			claims: ClaimAllocations{
				"claim-1": cpuset.New(0, 1, 2, 3),
			},
			expected: 4,
		},
		{
			name: "multiple claims totaling 10 CPUs",
			claims: ClaimAllocations{
				"claim-1": cpuset.New(0, 1, 2, 3),
				"claim-2": cpuset.New(4, 5, 6, 7, 8, 9),
			},
			expected: 10,
		},
		{
			name:     "empty claims",
			claims:   ClaimAllocations{},
			expected: 0,
		},
		{
			name: "single CPU",
			claims: ClaimAllocations{
				"claim-1": cpuset.New(0),
			},
			expected: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			total := tc.claims.TotalCPUs()
			if total != tc.expected {
				t.Errorf("expected %d CPUs, got %d", tc.expected, total)
			}
		})
	}
}
