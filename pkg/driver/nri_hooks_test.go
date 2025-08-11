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
	"context"
	"fmt"
	"testing"

	"github.com/containerd/nri/pkg/api"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/cpuset"
)

func TestParseDRAEnvToCPUSet(t *testing.T) {
	testCases := []struct {
		name                string
		envs                []string
		expectedCPUSet      cpuset.CPUSet
		expectedErrorString string
	}{
		{
			name:           "single valid env",
			envs:           []string{fmt.Sprintf("%s_claim_my-claim=%s", cdiEnvVarPrefix, "0-1")},
			expectedCPUSet: cpuset.New(0, 1),
		},
		{
			name: "multiple valid envs",
			envs: []string{
				fmt.Sprintf("%s_claim_claim1=%s", cdiEnvVarPrefix, "0,1"),
				fmt.Sprintf("%s_claim_claim2=%s", cdiEnvVarPrefix, "2,3"),
			},
			expectedCPUSet: cpuset.New(0, 1, 2, 3),
		},
		{
			name:           "no relevant envs",
			envs:           []string{"OTHER_ENV=value"},
			expectedCPUSet: cpuset.New(),
		},
		{
			name:                "malformed env - no equals",
			envs:                []string{fmt.Sprintf("%s_claim_my-claim", cdiEnvVarPrefix)},
			expectedErrorString: "malformed DRA env entry",
		},
		{
			name:                "malformed env - invalid cpuset",
			envs:                []string{fmt.Sprintf("%s_claim_my-claim=%s", cdiEnvVarPrefix, "a-b")},
			expectedErrorString: "failed to parse cpuset value",
		},
		{
			name:           "empty env",
			envs:           []string{},
			expectedCPUSet: cpuset.New(),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			set, err := parseDRAEnvToCPUSet(tc.envs)
			if tc.expectedErrorString != "" {
				require.Error(t, err)
				require.ErrorContains(t, err, tc.expectedErrorString)
			} else {
				require.NoError(t, err)
				require.True(t, set.Equals(tc.expectedCPUSet))
			}
		})
	}
}

func TestCreateContainer(t *testing.T) {
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	pod := &api.PodSandbox{Id: "pod-id-1", Name: "my-pod", Namespace: "my-ns", Uid: "pod-uid-1"}

	var infos []cpuinfo.CPUInfo
	for _, cpuID := range allCPUs.UnsortedList() {
		infos = append(infos, cpuinfo.CPUInfo{CpuID: cpuID, CoreID: cpuID, SocketID: 0, NumaNode: 0})
	}
	mockProvider := &mockCPUInfoProvider{cpuInfos: infos}

	// newTestContainer is a local helper to simplify test case definitions.
	newTestContainer := func(cpus string) *api.Container {
		var envs []string
		if cpus != "" {
			envs = append(envs, fmt.Sprintf("%s_claim_my-claim=%s", cdiEnvVarPrefix, cpus))
		}
		return &api.Container{
			Id:           "ctr-id-1",
			PodSandboxId: pod.Id,
			Name:         "my-ctr",
			Env:          envs,
		}
	}

	testCases := []struct {
		name                        string
		initialStore                *PodConfigStore
		container                   *api.Container
		sharedCPUUpdateRequired     bool
		expectedContainerAdjustment *api.ContainerAdjustment
		expectedContianerUpdates    []*api.ContainerUpdate
		expectedUpdateRequired      bool
	}{
		{
			name:         "guaranteed container triggers container adjustment with cpu's in resource claim",
			initialStore: NewPodConfigStore(mockProvider),
			container:    newTestContainer("0-3"),
			expectedContainerAdjustment: &api.ContainerAdjustment{
				Linux: &api.LinuxContainerAdjustment{Resources: &api.LinuxResources{Cpu: &api.LinuxCPU{Cpus: "0-3"}}},
			},
			expectedContianerUpdates: []*api.ContainerUpdate{},
			expectedUpdateRequired:   false,
		},
		{
			name:         "shared container triggers container adjustment with public cpus",
			initialStore: NewPodConfigStore(mockProvider),
			container:    newTestContainer(""),
			expectedContainerAdjustment: &api.ContainerAdjustment{
				Linux: &api.LinuxContainerAdjustment{Resources: &api.LinuxResources{Cpu: &api.LinuxCPU{Cpus: "0-7"}}},
			},
			expectedContianerUpdates: []*api.ContainerUpdate{},
			expectedUpdateRequired:   false,
		},
		{
			name: "guaranteed container triggers container adjustment and update for other shared container",
			initialStore: func() *PodConfigStore {
				s := NewPodConfigStore(mockProvider)
				s.SetContainerState("non-guaranteed-pod-1", NewContainerCPUState(CPUTypeShared, "shared-ctr-1", "1234", allCPUs))
				s.SetContainerState("non-guaranteed-pod-2", NewContainerCPUState(CPUTypeShared, "shared-ctr-2", "5678", allCPUs))
				return s
			}(),
			container: newTestContainer("0-1"),
			expectedContainerAdjustment: &api.ContainerAdjustment{
				Linux: &api.LinuxContainerAdjustment{Resources: &api.LinuxResources{Cpu: &api.LinuxCPU{Cpus: "0-1"}}},
			},
			expectedContianerUpdates: []*api.ContainerUpdate{
				{
					ContainerId: "1234",
					Linux:       &api.LinuxContainerUpdate{Resources: &api.LinuxResources{Cpu: &api.LinuxCPU{Cpus: "2-7"}}},
				},
				{
					ContainerId: "5678",
					Linux:       &api.LinuxContainerUpdate{Resources: &api.LinuxResources{Cpu: &api.LinuxCPU{Cpus: "2-7"}}},
				},
			},
			expectedUpdateRequired: false,
		},
		{
			name: "shared container triggers updates for all other shared containers when update is pending",
			initialStore: func() *PodConfigStore {
				s := NewPodConfigStore(mockProvider)
				s.SetContainerState("other-pod", NewContainerCPUState(CPUTypeShared, "shared-ctr", "1234", cpuset.New(0, 1)))
				return s
			}(),
			container:               newTestContainer(""),
			sharedCPUUpdateRequired: true,
			expectedContainerAdjustment: &api.ContainerAdjustment{
				Linux: &api.LinuxContainerAdjustment{Resources: &api.LinuxResources{Cpu: &api.LinuxCPU{Cpus: "0-7"}}},
			},
			expectedContianerUpdates: []*api.ContainerUpdate{
				{
					ContainerId: "1234",
					Linux:       &api.LinuxContainerUpdate{Resources: &api.LinuxResources{Cpu: &api.LinuxCPU{Cpus: "0-7"}}},
				},
			},
			expectedUpdateRequired: false, // Flag should be reset
		},
		{
			name:         "guaranteed container with malformed env falls back to shared",
			initialStore: NewPodConfigStore(mockProvider),
			container:    newTestContainer("invalid"),
			expectedContainerAdjustment: &api.ContainerAdjustment{
				Linux: &api.LinuxContainerAdjustment{Resources: &api.LinuxResources{Cpu: &api.LinuxCPU{Cpus: "0-7"}}},
			},
			expectedContianerUpdates: []*api.ContainerUpdate{},
			expectedUpdateRequired:   false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			driver := &CPUDriver{podConfigStore: tc.initialStore, cpuInfoProvider: mockProvider}
			setSharedCPUUpdateRequired(tc.sharedCPUUpdateRequired)

			adjust, updates, err := driver.CreateContainer(context.Background(), pod, tc.container)
			require.NoError(t, err)

			require.Equal(t, tc.expectedContainerAdjustment, adjust)
			require.ElementsMatch(t, tc.expectedContianerUpdates, updates)
			require.Equal(t, tc.expectedUpdateRequired, isSharedCPUUpdateRequired())
		})
	}
}

func TestRemoveContainer(t *testing.T) {
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	pod1 := &api.PodSandbox{Id: "pod-id-1", Name: "my-pod", Namespace: "my-ns", Uid: "pod-uid-1"}
	ctr1 := &api.Container{Id: "ctr-id-1", PodSandboxId: pod1.Id, Name: "my-ctr"}

	var infos []cpuinfo.CPUInfo
	for _, cpuID := range allCPUs.UnsortedList() {
		infos = append(infos, cpuinfo.CPUInfo{CpuID: cpuID, CoreID: cpuID, SocketID: 0, NumaNode: 0})
	}
	mockProvider := &mockCPUInfoProvider{cpuInfos: infos}

	testCases := []struct {
		name                   string
		initialStore           *PodConfigStore
		removePod              bool
		expectedUpdateRequired bool
	}{
		{
			name: "Remove guaranteed container sets update required for shared containers",
			initialStore: func() *PodConfigStore {
				s := NewPodConfigStore(mockProvider)
				s.SetContainerState(types.UID(pod1.Uid), NewContainerCPUState(CPUTypeGuaranteed, ctr1.Name, types.UID(ctr1.Id), cpuset.New(0, 1)))
				return s
			}(),
			removePod:              false,
			expectedUpdateRequired: true,
		},
		{
			name: "Remove non-guaranteed container does not set update required",
			initialStore: func() *PodConfigStore {
				s := NewPodConfigStore(mockProvider)
				s.SetContainerState(types.UID(pod1.Uid), NewContainerCPUState(CPUTypeShared, ctr1.Name, types.UID(ctr1.Id), cpuset.New(0, 1, 2, 3)))
				return s
			}(),
			removePod:              false,
			expectedUpdateRequired: false,
		},
		{
			name: "Remove guaranteed pod sets update required for shared containers",
			initialStore: func() *PodConfigStore {
				s := NewPodConfigStore(mockProvider)
				s.SetContainerState(types.UID(pod1.Uid), NewContainerCPUState(CPUTypeGuaranteed, ctr1.Name, types.UID(ctr1.Id), cpuset.New(0, 1)))
				return s
			}(),
			removePod:              true,
			expectedUpdateRequired: true,
		},
		{
			name: "Remove non-guaranteed pod does not set update required",
			initialStore: func() *PodConfigStore {
				s := NewPodConfigStore(mockProvider)
				s.SetContainerState(types.UID(pod1.Uid), NewContainerCPUState(CPUTypeGuaranteed, ctr1.Name, types.UID(ctr1.Id), cpuset.New(0, 1, 2, 3)))
				return s
			}(),
			removePod:              true,
			expectedUpdateRequired: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			driver := &CPUDriver{podConfigStore: tc.initialStore, cpuInfoProvider: mockProvider}
			setSharedCPUUpdateRequired(false) // Reset before each test

			var err error
			if tc.removePod {
				err = driver.RemovePodSandbox(context.Background(), pod1)
			} else {
				err = driver.RemoveContainer(context.Background(), pod1, ctr1)
			}
			require.NoError(t, err)

			require.Equal(t, tc.expectedUpdateRequired, isSharedCPUUpdateRequired())
		})
	}
}

func TestNRISynchronize(t *testing.T) {
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	var infos []cpuinfo.CPUInfo
	for _, cpuID := range allCPUs.UnsortedList() {
		infos = append(infos, cpuinfo.CPUInfo{CpuID: cpuID})
	}
	mockProvider := &mockCPUInfoProvider{cpuInfos: infos}

	pod1 := &api.PodSandbox{Id: "pod-id-1", Name: "my-pod-1", Namespace: "my-ns", Uid: "pod-uid-1"}
	pod2 := &api.PodSandbox{Id: "pod-id-2", Name: "my-pod-2", Namespace: "my-ns", Uid: "pod-uid-2"}

	testCases := []struct {
		name               string
		initialStore       *PodConfigStore
		runtimePods        []*api.PodSandbox
		runtimeCtrs        []*api.Container
		expectStoreCleared bool
		expectedError      bool
	}{
		{
			name: "empty runtime state clears the store",
			initialStore: func() *PodConfigStore {
				s := NewPodConfigStore(mockProvider)
				s.SetContainerState(types.UID(pod1.Uid), NewContainerCPUState(CPUTypeGuaranteed, "stale-ctr", "stale-id", cpuset.New(0)))
				return s
			}(),
			runtimePods:        []*api.PodSandbox{},
			runtimeCtrs:        []*api.Container{},
			expectStoreCleared: true,
		},
		{
			name:         "mixed containers across multiple pods",
			initialStore: NewPodConfigStore(mockProvider),
			runtimePods:  []*api.PodSandbox{pod1, pod2},
			runtimeCtrs: []*api.Container{
				{Id: "p1-guaranteed", PodSandboxId: pod1.Id, Name: "guaranteed-ctr", Env: []string{fmt.Sprintf("%s_claim_A=%s", cdiEnvVarPrefix, "0,1")}},
				{Id: "p1-shared", PodSandboxId: pod1.Id, Name: "shared-ctr"},
				{Id: "p2-shared", PodSandboxId: pod2.Id, Name: "shared-ctr"},
			},
			expectStoreCleared: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			driver := &CPUDriver{podConfigStore: tc.initialStore, cpuInfoProvider: mockProvider}
			_, err := driver.Synchronize(context.Background(), tc.runtimePods, tc.runtimeCtrs)

			if tc.expectedError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			if tc.expectStoreCleared {
				require.Empty(t, driver.podConfigStore.configs)
			} else {
				// Verify that the store has been populated
				for _, pod := range tc.runtimePods {
					for _, container := range tc.runtimeCtrs {
						if container.PodSandboxId != pod.Id {
							continue
						}
						state := driver.podConfigStore.GetContainerState(types.UID(pod.Uid), container.Name)
						require.NotNil(t, state)
					}
				}
			}
		})
	}
}
