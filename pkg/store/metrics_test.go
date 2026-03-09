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

package store

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/cpuset"
)

func TestMetricsGaugesOnAllocationStoreChanges(t *testing.T) {
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	reserved := cpuset.New(0, 1)

	tests := []struct {
		name                      string
		allocations               map[types.UID]cpuset.CPUSet
		removals                  []types.UID
		expectedAllocatedCPUs     float64
		expectedAvailableCPUs     float64
		expectedReservedCPUs      float64
		expectedActiveClaimsGauge float64
	}{
		{
			name:                      "initial state with no allocations",
			expectedAllocatedCPUs:     0,
			expectedAvailableCPUs:     6,
			expectedReservedCPUs:      2,
			expectedActiveClaimsGauge: 0,
		},
		{
			name: "single allocation",
			allocations: map[types.UID]cpuset.CPUSet{
				"claim-1": cpuset.New(2, 3),
			},
			expectedAllocatedCPUs:     2,
			expectedAvailableCPUs:     4,
			expectedReservedCPUs:      2,
			expectedActiveClaimsGauge: 1,
		},
		{
			name: "multiple allocations",
			allocations: map[types.UID]cpuset.CPUSet{
				"claim-1": cpuset.New(2, 3),
				"claim-2": cpuset.New(4, 5),
			},
			expectedAllocatedCPUs:     4,
			expectedAvailableCPUs:     2,
			expectedReservedCPUs:      2,
			expectedActiveClaimsGauge: 2,
		},
		{
			name: "allocate then remove",
			allocations: map[types.UID]cpuset.CPUSet{
				"claim-1": cpuset.New(2, 3),
				"claim-2": cpuset.New(4, 5),
			},
			removals:                  []types.UID{"claim-1"},
			expectedAllocatedCPUs:     2,
			expectedAvailableCPUs:     4,
			expectedReservedCPUs:      2,
			expectedActiveClaimsGauge: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newTestCPUAllocation(allCPUs, reserved)
			for uid, cpus := range tc.allocations {
				s.AddResourceClaimAllocation(uid, cpus)
			}
			for _, uid := range tc.removals {
				s.RemoveResourceClaimAllocation(uid)
			}

			require.InDelta(t, tc.expectedAllocatedCPUs, testutil.ToFloat64(allocatedCPUsGauge), 0.01)
			require.InDelta(t, tc.expectedAvailableCPUs, testutil.ToFloat64(availableCPUsGauge), 0.01)
			require.InDelta(t, tc.expectedReservedCPUs, testutil.ToFloat64(reservedCPUsGauge), 0.01)
			require.InDelta(t, tc.expectedActiveClaimsGauge, testutil.ToFloat64(activeResourceClaimsGauge), 0.01)
		})
	}
}
