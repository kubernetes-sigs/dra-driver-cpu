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

func newPodConfigStore(t *testing.T) (*PodConfigStore, cpuset.CPUSet) {
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	var infos []cpuinfo.CPUInfo
	for _, cpuID := range allCPUs.UnsortedList() {
		infos = append(infos, cpuinfo.CPUInfo{CpuID: cpuID, CoreID: cpuID, SocketID: 0, NumaNode: 0})
	}
	mockProvider := &mockCPUInfoProvider{cpuInfos: infos}
	store := NewPodConfigStore(mockProvider)
	require.NotNil(t, store)
	require.True(t, store.allCPUs.Equals(allCPUs))
	require.True(t, store.publicCPUs.Equals(allCPUs))
	return store, allCPUs
}

func TestContainerState(t *testing.T) {
	store, _ := newPodConfigStore(t)
	podUID := types.UID("pod-uid-1")
	ctrName := "ctr-name-1"
	state := NewContainerCPUState(CPUTypeGuaranteed, ctrName, "ctr-id-1", cpuset.New(0, 1))

	// Get non-existent state
	require.Nil(t, store.GetContainerState(podUID, ctrName))

	// Set and get state
	store.SetContainerState(podUID, state)
	gotState := store.GetContainerState(podUID, ctrName)
	require.Equal(t, state, gotState)
}

func TestRemoveContainerState(t *testing.T) {
	store, allCPUs := newPodConfigStore(t)
	podUID := types.UID("pod-uid-1")
	ctrName := "ctr-name-1"
	guaranteedCPUs := cpuset.New(0, 1)
	guaranteedState := NewContainerCPUState(CPUTypeGuaranteed, ctrName, "ctr-id-1", guaranteedCPUs)

	// Setup: add a container
	store.SetContainerState(podUID, guaranteedState)
	publicCPUs := store.GetPublicCPUs()
	require.False(t, publicCPUs.Equals(allCPUs), "Public CPUs should be reduced")
	require.True(t, publicCPUs.Equals(allCPUs.Difference(guaranteedCPUs)), "Public CPUs should exclude guaranteed CPUs")

	// Remove container
	store.RemoveContainerState(podUID, ctrName)
	require.Nil(t, store.GetContainerState(podUID, ctrName))
	require.True(t, store.GetPublicCPUs().Equals(allCPUs), "Public CPUs should be restored")

	// Remove non-existent container, should not panic
	store.RemoveContainerState(podUID, "non-existent-ctr")
	require.True(t, store.GetPublicCPUs().Equals(allCPUs))
}

func TestDeletePodState(t *testing.T) {
	store, allCPUs := newPodConfigStore(t)
	podUID := types.UID("pod-uid-1")
	guaranteedState := NewContainerCPUState(CPUTypeGuaranteed, "ctr-1", "id-1", cpuset.New(0, 1))
	sharedState := NewContainerCPUState(CPUTypeShared, "ctr-2", "id-2", cpuset.New())

	// Setup: add a pod with two containers
	store.SetContainerState(podUID, guaranteedState)
	store.SetContainerState(podUID, sharedState)
	_, podExists := store.configs[podUID]
	require.True(t, podExists)

	// Delete pod
	store.DeletePodState(podUID)
	_, podExists = store.configs[podUID]
	require.False(t, podExists)
	require.True(t, store.GetPublicCPUs().Equals(allCPUs), "Public CPUs should be restored")

	// Deleting non-existent pod should be a no-op
	store.DeletePodState(podUID)
}

func TestGetContainersWithSharedCPUs(t *testing.T) {
	sharedState1 := NewContainerCPUState(CPUTypeShared, "c1", "id1", cpuset.New())
	sharedState2 := NewContainerCPUState(CPUTypeShared, "c2", "id2", cpuset.New())
	guaranteedState := NewContainerCPUState(CPUTypeGuaranteed, "c3", "id3", cpuset.New(2, 3))

	testCases := []struct {
		name     string
		setup    func(s *PodConfigStore)
		wantUIDs []types.UID
	}{
		{
			name: "some shared, some guaranteed",
			setup: func(s *PodConfigStore) {
				s.SetContainerState("pod1", sharedState1)
				s.SetContainerState("pod2", sharedState2)
				s.SetContainerState("pod1", guaranteedState)
			},
			wantUIDs: []types.UID{sharedState1.containerUID, sharedState2.containerUID},
		},
		{
			name: "only guaranteed",
			setup: func(s *PodConfigStore) {
				s.SetContainerState("pod1", guaranteedState)
			},
			wantUIDs: []types.UID{},
		},
		{
			name:     "no containers",
			setup:    func(s *PodConfigStore) {},
			wantUIDs: []types.UID{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := newPodConfigStore(t)
			tc.setup(store)
			gotUIDs := store.GetContainersWithSharedCPUs()
			require.ElementsMatch(t, tc.wantUIDs, gotUIDs)
		})
	}
}

func TestIsContainerGuaranteed(t *testing.T) {
	store, _ := newPodConfigStore(t)
	podUID := types.UID("pod-uid-1")
	guaranteedCtr := "guaranteed-ctr"
	sharedCtr := "shared-ctr"
	store.SetContainerState(podUID, NewContainerCPUState(CPUTypeGuaranteed, guaranteedCtr, "id1", cpuset.New(0, 1)))
	store.SetContainerState(podUID, NewContainerCPUState(CPUTypeShared, sharedCtr, "id2", cpuset.New()))

	require.True(t, store.IsContainerGuaranteed(podUID, guaranteedCtr))
	require.False(t, store.IsContainerGuaranteed(podUID, sharedCtr))
	require.False(t, store.IsContainerGuaranteed(podUID, "non-existent-ctr"))
	require.False(t, store.IsContainerGuaranteed("non-existent-pod", guaranteedCtr))
}

func TestIsPodGuaranteed(t *testing.T) {
	store, _ := newPodConfigStore(t)
	guaranteedPod := types.UID("guaranteed-pod")
	sharedPod := types.UID("shared-pod")
	mixedPod := types.UID("mixed-pod")

	// Pod with only guaranteed containers
	store.SetContainerState(guaranteedPod, NewContainerCPUState(CPUTypeGuaranteed, "c1", "id1", cpuset.New(0, 1)))

	// Pod with only shared containers
	store.SetContainerState(sharedPod, NewContainerCPUState(CPUTypeShared, "c2", "id2", cpuset.New()))

	// Pod with both guaranteed and shared containers
	store.SetContainerState(mixedPod, NewContainerCPUState(CPUTypeGuaranteed, "c3", "id3", cpuset.New(2, 3)))
	store.SetContainerState(mixedPod, NewContainerCPUState(CPUTypeShared, "c4", "id4", cpuset.New()))

	// A pod is considered guaranteed if it has at least one guaranteed container.
	require.True(t, store.IsPodGuaranteed(guaranteedPod))
	require.False(t, store.IsPodGuaranteed(sharedPod))
	require.True(t, store.IsPodGuaranteed(mixedPod))
}
