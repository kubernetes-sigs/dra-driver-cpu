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
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
)

func TestSetAndGetContainerState(t *testing.T) {
	store := NewPodConfigStore()
	podUID := types.UID("pod-uid-1")
	ctrName := "ctr-name-1"
	state := NewContainerState(ctrName, "ctr-uid-1", types.UID("claim-uid-1"))

	// Get non-existent state
	require.Nil(t, store.GetContainerState(podUID, ctrName))

	// Set and get state
	store.SetContainerState(podUID, state)
	gotState := store.GetContainerState(podUID, ctrName)
	require.Equal(t, state, gotState)
}

func TestRemoveContainerState(t *testing.T) {
	store := NewPodConfigStore()
	podUID := types.UID("pod-uid-1")
	ctrName1 := "ctr-name-1"
	ctrName2 := "ctr-name-2"
	state1 := NewContainerState(ctrName1, "ctr-uid-1", types.UID("claim-uid-1"))
	state2 := NewContainerState(ctrName2, "ctr-uid-2")

	// Setup: add a pod with two containers
	store.SetContainerState(podUID, state1)
	store.SetContainerState(podUID, state2)
	require.NotNil(t, store.GetContainerState(podUID, ctrName1))
	require.NotNil(t, store.GetContainerState(podUID, ctrName2))

	// Remove one container
	store.RemoveContainerState(podUID, ctrName1)
	require.Nil(t, store.GetContainerState(podUID, ctrName1))
	require.NotNil(t, store.GetContainerState(podUID, ctrName2), "other container should still exist")

	// Remove the second container, which should remove the pod entry
	store.RemoveContainerState(podUID, ctrName2)
	require.Nil(t, store.GetContainerState(podUID, ctrName2))
	_, podExists := store.configs[podUID]
	require.False(t, podExists, "pod entry should be gone after last container is removed")

	// Remove non-existent container, should not panic
	store.RemoveContainerState(podUID, "non-existent-ctr")
}

func TestGetSharedCPUContainerUIDs(t *testing.T) {
	sharedState1 := NewContainerState("c1", "id1")
	sharedState2 := NewContainerState("c2", "id2")
	guaranteedState := NewContainerState("c3", "id3", types.UID("claim-uid-1"))

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
			store := NewPodConfigStore()
			tc.setup(store)
			gotUIDs := store.GetContainersWithSharedCPUs()
			require.ElementsMatch(t, tc.wantUIDs, gotUIDs)
		})
	}
}

func TestPodHasExclusiveCPUAllocation(t *testing.T) {
	guaranteedState := NewContainerState("guaranteed-ctr", "gid1", types.UID("claim-uid-1"))
	sharedState := NewContainerState("shared-ctr", "sid1")

	testCases := []struct {
		name           string
		podUID         types.UID
		setup          func(s *PodConfigStore)
		wantGuaranteed bool
	}{
		{
			name:           "pod not in store",
			podUID:         "non-existent-pod",
			setup:          func(s *PodConfigStore) {},
			wantGuaranteed: false,
		},
		{
			name:   "pod with only shared containers",
			podUID: "pod1",
			setup: func(s *PodConfigStore) {
				s.SetContainerState("pod1", sharedState)
			},
			wantGuaranteed: false,
		},
		{
			name:   "pod with only guaranteed containers",
			podUID: "pod1",
			setup: func(s *PodConfigStore) {
				s.SetContainerState("pod1", guaranteedState)
			},
			wantGuaranteed: true,
		},
		{
			name:   "pod with mixed containers",
			podUID: "pod1",
			setup: func(s *PodConfigStore) {
				s.SetContainerState("pod1", sharedState)
				s.SetContainerState("pod1", guaranteedState)
			},
			wantGuaranteed: true,
		},
		{
			name:   "pod with multiple shared containers",
			podUID: "pod1",
			setup: func(s *PodConfigStore) {
				s.SetContainerState("pod1", NewContainerState("c1", "id1"))
				s.SetContainerState("pod1", NewContainerState("c2", "id2"))
			},
			wantGuaranteed: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewPodConfigStore()
			tc.setup(store)
			gotGuaranteed := store.PodHasExclusiveCPUAllocation(tc.podUID)
			require.Equal(t, tc.wantGuaranteed, gotGuaranteed)
		})
	}
}

func TestSharedCPUContainersCacheConsistency(t *testing.T) {
	store := NewPodConfigStore()

	store.SetContainerState("pod1", NewContainerState("c1", "id1"))
	store.SetContainerState("pod1", NewContainerState("c2", "id2"))
	store.SetContainerState("pod2", NewContainerState("c3", "id3"))

	sharedUIDs := store.GetContainersWithSharedCPUs()
	require.Len(t, sharedUIDs, 3)

	store.SetContainerState("pod3", NewContainerState("c4", "id4", "claim-1"))
	sharedUIDs = store.GetContainersWithSharedCPUs()
	require.Len(t, sharedUIDs, 3)

	store.SetContainerState("pod1", NewContainerState("c1", "id1", "claim-2"))
	sharedUIDs = store.GetContainersWithSharedCPUs()
	require.Len(t, sharedUIDs, 2)
	require.NotContains(t, sharedUIDs, types.UID("id1"))

	store.RemoveContainerState("pod1", "c2")
	sharedUIDs = store.GetContainersWithSharedCPUs()
	require.Len(t, sharedUIDs, 1)
	require.ElementsMatch(t, []types.UID{"id3"}, sharedUIDs)

	store.RemoveContainerState("pod2", "c3")
	sharedUIDs = store.GetContainersWithSharedCPUs()
	require.Len(t, sharedUIDs, 0)
}

func getContainersWithSharedCPUsNaive(configs map[types.UID]map[string]*ContainerState) []types.UID {
	var result []types.UID
	for _, containers := range configs {
		for _, state := range containers {
			if len(state.resourceClaimUIDs) == 0 {
				result = append(result, state.containerUID)
			}
		}
	}
	return result
}

func BenchmarkGetContainersWithSharedCPUs(b *testing.B) {
	testCases := []struct {
		name          string
		numPods       int
		ctrsPerPod    int
		sharedPercent int
	}{
		{"10_pods_50pct_shared", 10, 2, 50},
		{"100_pods_50pct_shared", 100, 2, 50},
		{"500_pods_50pct_shared", 500, 2, 50},
		{"500_pods_90pct_shared", 500, 2, 90},
	}

	for _, tc := range testCases {
		configs := make(map[types.UID]map[string]*ContainerState)
		store := NewPodConfigStore()

		ctrIndex := 0
		for i := 0; i < tc.numPods; i++ {
			podUID := types.UID(fmt.Sprintf("pod-%d", i))
			configs[podUID] = make(map[string]*ContainerState)

			for j := 0; j < tc.ctrsPerPod; j++ {
				ctrName := fmt.Sprintf("ctr-%d", j)
				ctrUID := types.UID(fmt.Sprintf("ctr-uid-%d", ctrIndex))

				var state *ContainerState
				if (ctrIndex*100)/(tc.numPods*tc.ctrsPerPod) < tc.sharedPercent {
					state = NewContainerState(ctrName, ctrUID)
				} else {
					state = NewContainerState(ctrName, ctrUID, types.UID(fmt.Sprintf("claim-%d", ctrIndex)))
				}
				configs[podUID][ctrName] = state
				store.SetContainerState(podUID, state)
				ctrIndex++
			}
		}

		b.Run(tc.name+"/naive", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = getContainersWithSharedCPUsNaive(configs)
			}
		})

		b.Run(tc.name+"/optimized", func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_ = store.GetContainersWithSharedCPUs()
			}
		})
	}
}
