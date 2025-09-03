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

package driver

import (
	"testing"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/cpuset"
)

func newTestCPUAllocationStore() (*CPUAllocationStore, cpuset.CPUSet) {
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	var infos []cpuinfo.CPUInfo
	for _, cpuID := range allCPUs.UnsortedList() {
		infos = append(infos, cpuinfo.CPUInfo{CpuID: cpuID, CoreID: cpuID, SocketID: 0, NumaNode: 0})
	}
	mockProvider := &mockCPUInfoProvider{cpuInfos: infos}
	return NewCPUAllocationStore(mockProvider), allCPUs
}

func TestCPUAllocationStoreResourceClaimAllocation(t *testing.T) {
	store, _ := newTestCPUAllocationStore()
	claimUID := types.UID("claim-uid-1")
	cpus := cpuset.New(0, 1)

	// Add allocation
	store.AddResourceClaimAllocation(claimUID, cpus)
	require.Contains(t, store.resourceClaimAllocations, claimUID)
	require.True(t, store.resourceClaimAllocations[claimUID].Equals(cpus))

	// Remove allocation
	store.RemoveResourceClaimAllocation(claimUID)
	require.NotContains(t, store.resourceClaimAllocations, claimUID)

	// Remove non-existent allocation
	store.RemoveResourceClaimAllocation(types.UID("non-existent"))
}

func TestCPUAllocationStoreGetSharedCPUs(t *testing.T) {
	store, allCPUs := newTestCPUAllocationStore()

	// No allocations
	require.True(t, store.GetSharedCPUs().Equals(allCPUs))

	// With allocations
	claimUID1 := types.UID("claim-uid-1")
	cpus1 := cpuset.New(1, 2)
	store.AddResourceClaimAllocation(claimUID1, cpus1)
	expectedShared := allCPUs.Difference(cpus1)
	require.True(t, store.GetSharedCPUs().Equals(expectedShared))

	claimUID2 := types.UID("claim-uid-2")
	cpus2 := cpuset.New(3, 4)
	store.AddResourceClaimAllocation(claimUID2, cpus2)
	expectedShared = expectedShared.Difference(cpus2)
	require.True(t, store.GetSharedCPUs().Equals(expectedShared))
}
