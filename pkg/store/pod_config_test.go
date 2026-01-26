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

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
)

func TestSetAndGetContainerState(t *testing.T) {
	store := NewPodConfig()
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
	store := NewPodConfig()
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
		setup    func(s *PodConfig)
		wantUIDs []types.UID
	}{
		{
			name: "some shared, some guaranteed",
			setup: func(s *PodConfig) {
				s.SetContainerState("pod1", sharedState1)
				s.SetContainerState("pod2", sharedState2)
				s.SetContainerState("pod1", guaranteedState)
			},
			wantUIDs: []types.UID{sharedState1.containerUID, sharedState2.containerUID},
		},
		{
			name: "only guaranteed",
			setup: func(s *PodConfig) {
				s.SetContainerState("pod1", guaranteedState)
			},
			wantUIDs: []types.UID{},
		},
		{
			name:     "no containers",
			setup:    func(s *PodConfig) {},
			wantUIDs: []types.UID{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			store := NewPodConfig()
			tc.setup(store)
			gotUIDs := store.GetContainersWithSharedCPUs()
			require.ElementsMatch(t, tc.wantUIDs, gotUIDs)
		})
	}
}
