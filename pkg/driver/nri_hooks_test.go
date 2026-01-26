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
	"sort"
	"testing"

	"github.com/containerd/nri/pkg/api"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/cpuset"
)

func TestParseDRAEnvToClaimAllocations(t *testing.T) {
	testCases := []struct {
		name                  string
		envs                  []string
		expectedAllocations   map[types.UID]cpuset.CPUSet
		expectedErrorContains string
	}{
		{
			name: "single valid env",
			envs: []string{fmt.Sprintf("%s_claim-uid-1=%s", cdiEnvVarPrefix, "0-1")},
			expectedAllocations: map[types.UID]cpuset.CPUSet{
				"claim-uid-1": cpuset.New(0, 1),
			},
		},
		{
			name: "multiple valid envs",
			envs: []string{
				fmt.Sprintf("%s_claim-uid-1=%s", cdiEnvVarPrefix, "0,1"),
				fmt.Sprintf("%s_claim-uid-2=%s", cdiEnvVarPrefix, "2,3"),
			},
			expectedAllocations: map[types.UID]cpuset.CPUSet{
				"claim-uid-1": cpuset.New(0, 1),
				"claim-uid-2": cpuset.New(2, 3),
			},
		},
		{
			name:                "no relevant envs",
			envs:                []string{"OTHER_ENV=value"},
			expectedAllocations: map[types.UID]cpuset.CPUSet{},
		},
		{
			name:                  "malformed env - no equals",
			envs:                  []string{fmt.Sprintf("%s_claim-uid-1", cdiEnvVarPrefix)},
			expectedErrorContains: "malformed DRA env entry",
		},
		{
			name:                  "malformed env - invalid cpuset",
			envs:                  []string{fmt.Sprintf("%s_claim-uid-1=%s", cdiEnvVarPrefix, "a-b")},
			expectedErrorContains: "failed to parse cpuset value",
		},
		{
			name:                "empty env",
			envs:                []string{},
			expectedAllocations: map[types.UID]cpuset.CPUSet{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			allocations, err := parseDRAEnvToClaimAllocations(tc.envs)
			if tc.expectedErrorContains != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedErrorContains)
			} else {
				require.NoError(t, err)
				require.Equal(t, len(tc.expectedAllocations), len(allocations))
				for uid, expectedCpus := range tc.expectedAllocations {
					actualCpus, ok := allocations[uid]
					require.True(t, ok)
					require.True(t, expectedCpus.Equals(actualCpus))
				}
			}
		})
	}
}

func TestCreateContainer(t *testing.T) {
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	pod := &api.PodSandbox{Id: "pod-id-1", Name: "my-pod", Namespace: "my-ns", Uid: "pod-uid-1"}
	claimUID := "claim-uid-1"

	var infos []cpuinfo.CPUInfo
	for _, cpuID := range allCPUs.UnsortedList() {
		infos = append(infos, cpuinfo.CPUInfo{CpuID: cpuID, CoreID: cpuID, SocketID: 0, NUMANodeID: 0})
	}
	mockProvider := &cpuinfo.MockCPUInfoProvider{CPUInfos: infos}
	topo, _ := mockProvider.GetCPUTopology()

	// newTestContainer is a local helper to simplify test case definitions.
	newTestContainer := func(claimUID, cpus string) *api.Container {
		var envs []string
		if cpus != "" {
			envs = append(envs, fmt.Sprintf("%s_%s=%s", cdiEnvVarPrefix, claimUID, cpus))
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
		podConfigStore              *PodConfigStore
		cpuAllocationStore          *CPUAllocationStore
		claimTracker                *store.ClaimTracker
		container                   *api.Container
		expectedContainerAdjustment *api.ContainerAdjustment
		expectedContainerUpdates    []*api.ContainerUpdate
	}{
		{
			name:               "guaranteed container triggers container adjustment with cpus in resource claim",
			podConfigStore:     NewPodConfigStore(),
			cpuAllocationStore: NewCPUAllocationStore(topo, cpuset.New()),
			claimTracker:       store.NewClaimTracker(),
			container:          newTestContainer(claimUID, "0-3"),
			expectedContainerAdjustment: &api.ContainerAdjustment{
				Linux: &api.LinuxContainerAdjustment{Resources: &api.LinuxResources{Cpu: &api.LinuxCPU{Cpus: "0-3"}}},
			},
			expectedContainerUpdates: []*api.ContainerUpdate{},
		},
		{
			name:               "shared container triggers container adjustment with all cpus",
			podConfigStore:     NewPodConfigStore(),
			cpuAllocationStore: NewCPUAllocationStore(topo, cpuset.New()),
			claimTracker:       store.NewClaimTracker(),
			container:          newTestContainer("", ""),
			expectedContainerAdjustment: &api.ContainerAdjustment{
				Linux: &api.LinuxContainerAdjustment{Resources: &api.LinuxResources{Cpu: &api.LinuxCPU{Cpus: "0-7"}}},
			},
			expectedContainerUpdates: []*api.ContainerUpdate{},
		},
		{
			name: "guaranteed container triggers container adjustment and update for other shared container",
			podConfigStore: func() *PodConfigStore {
				store := NewPodConfigStore()
				store.SetContainerState("shared-pod-1", NewContainerState("shared-ctr-1", "shared-uid-1"))
				store.SetContainerState("shared-pod-2", NewContainerState("shared-ctr-2", "shared-uid-2"))
				return store
			}(),
			cpuAllocationStore: func() *CPUAllocationStore {
				store := NewCPUAllocationStore(topo, cpuset.New())
				store.AddResourceClaimAllocation(types.UID(claimUID), cpuset.New(2, 3))
				return store
			}(),
			claimTracker: store.NewClaimTracker(),
			container:    newTestContainer(claimUID, "2-3"),
			expectedContainerAdjustment: &api.ContainerAdjustment{
				Linux: &api.LinuxContainerAdjustment{Resources: &api.LinuxResources{Cpu: &api.LinuxCPU{Cpus: "2-3"}}},
			},
			expectedContainerUpdates: []*api.ContainerUpdate{
				{
					ContainerId: "shared-uid-1",
					Linux:       &api.LinuxContainerUpdate{Resources: &api.LinuxResources{Cpu: &api.LinuxCPU{Cpus: "0-1,4-7"}}},
				},
				{
					ContainerId: "shared-uid-2",
					Linux:       &api.LinuxContainerUpdate{Resources: &api.LinuxResources{Cpu: &api.LinuxCPU{Cpus: "0-1,4-7"}}},
				},
			},
		},
		{
			name:               "guaranteed container with malformed env falls back to shared",
			podConfigStore:     NewPodConfigStore(),
			cpuAllocationStore: NewCPUAllocationStore(topo, cpuset.New()),
			claimTracker:       store.NewClaimTracker(),
			container: &api.Container{
				Id:           "ctr-id-1",
				PodSandboxId: pod.Id,
				Name:         "my-ctr",
				Env:          []string{fmt.Sprintf("%s_%s=%s", cdiEnvVarPrefix, claimUID, "a-b")},
			},
			expectedContainerAdjustment: &api.ContainerAdjustment{
				Linux: &api.LinuxContainerAdjustment{Resources: &api.LinuxResources{Cpu: &api.LinuxCPU{Cpus: "0-7"}}},
			},
			expectedContainerUpdates: []*api.ContainerUpdate{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			driver := &CPUDriver{
				podConfigStore:     tc.podConfigStore,
				cpuAllocationStore: tc.cpuAllocationStore,
				claimTracker:       tc.claimTracker,
			}
			adjust, updates, err := driver.CreateContainer(context.Background(), pod, tc.container)
			require.NoError(t, err)

			require.Equal(t, tc.expectedContainerAdjustment, adjust)
			require.ElementsMatch(t, tc.expectedContainerUpdates, updates)
		})
	}
}

func TestStopContainer(t *testing.T) {
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	pod1 := &api.PodSandbox{Id: "pod-id-1", Name: "my-pod-1", Namespace: "my-ns", Uid: "pod-uid-1"}
	ctr1 := &api.Container{Id: "ctr-id-1", PodSandboxId: pod1.Id, Name: "my-ctr-1"}
	pod2 := &api.PodSandbox{Id: "pod-id-2", Name: "my-pod-2", Namespace: "my-ns", Uid: "pod-uid-2"}
	ctr2 := &api.Container{Id: "ctr-id-2", PodSandboxId: pod2.Id, Name: "my-ctr-2"}

	var infos []cpuinfo.CPUInfo
	for _, cpuID := range allCPUs.UnsortedList() {
		infos = append(infos, cpuinfo.CPUInfo{CpuID: cpuID, CoreID: cpuID, SocketID: 0, NUMANodeID: 0})
	}
	mockProvider := &cpuinfo.MockCPUInfoProvider{CPUInfos: infos}
	topo, _ := mockProvider.GetCPUTopology()

	testCases := []struct {
		name               string
		driver             *CPUDriver
		expectedUpdatesFor []string
	}{
		{
			name: "Stop guaranteed container sets update required for shared containers",
			driver: func() *CPUDriver {
				driver := &CPUDriver{
					podConfigStore:     NewPodConfigStore(),
					cpuAllocationStore: NewCPUAllocationStore(topo, cpuset.New()),
					claimTracker:       store.NewClaimTracker(),
					cpuTopology:        topo,
				}
				driver.podConfigStore.SetContainerState(types.UID(pod1.Uid), NewContainerState(ctr1.Name, types.UID(ctr1.Id), types.UID("claim-uid-1")))
				driver.podConfigStore.SetContainerState(types.UID(pod2.Uid), NewContainerState(ctr2.Name, types.UID(ctr2.Id)))
				return driver
			}(),
			expectedUpdatesFor: []string{ctr2.Id},
		},
		{
			name: "Stop non-guaranteed container does not set update required",
			driver: func() *CPUDriver {
				driver := &CPUDriver{
					podConfigStore:     NewPodConfigStore(),
					cpuAllocationStore: NewCPUAllocationStore(topo, cpuset.New()),
					claimTracker:       store.NewClaimTracker(),
					cpuTopology:        topo,
				}
				driver.podConfigStore.SetContainerState(types.UID(pod1.Uid), NewContainerState(ctr1.Name, types.UID(ctr1.Id)))
				driver.podConfigStore.SetContainerState(types.UID(pod2.Uid), NewContainerState(ctr2.Name, types.UID(ctr2.Id)))
				return driver
			}(),
			expectedUpdatesFor: []string{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			sort.Strings(tc.expectedUpdatesFor)
			upd, err := tc.driver.StopContainer(context.Background(), pod1, ctr1)
			require.NoError(t, err)
			require.Equal(t, containerIDsFromUpdates(upd), tc.expectedUpdatesFor)
		})
	}
}

func TestNRISynchronize(t *testing.T) {
	allCPUs := cpuset.New(0, 1, 2, 3, 4, 5, 6, 7)
	var infos []cpuinfo.CPUInfo
	for _, cpuID := range allCPUs.UnsortedList() {
		infos = append(infos, cpuinfo.CPUInfo{CpuID: cpuID})
	}
	mockProvider := &cpuinfo.MockCPUInfoProvider{CPUInfos: infos}
	topo, _ := mockProvider.GetCPUTopology()

	pod1 := &api.PodSandbox{Id: "pod-id-1", Name: "my-pod-1", Namespace: "my-ns", Uid: "pod-uid-1"}
	pod2 := &api.PodSandbox{Id: "pod-id-2", Name: "my-pod-2", Namespace: "my-ns", Uid: "pod-uid-2"}

	testCases := []struct {
		name          string
		driver        *CPUDriver
		runtimePods   []*api.PodSandbox
		runtimeCtrs   []*api.Container
		expectedError bool
	}{
		{
			name: "empty runtime state clears the store",
			driver: func() *CPUDriver {
				driver := &CPUDriver{
					podConfigStore:     NewPodConfigStore(),
					cpuAllocationStore: NewCPUAllocationStore(topo, cpuset.New()),
					claimTracker:       store.NewClaimTracker(),
					cpuTopology:        topo,
				}
				driver.podConfigStore.SetContainerState(types.UID(pod1.Uid), NewContainerState("stale-ctr", "stale-id", types.UID("stale-claim")))
				return driver
			}(),
			runtimePods: []*api.PodSandbox{},
			runtimeCtrs: []*api.Container{},
		},
		{
			name: "mixed containers across multiple pods",
			driver: &CPUDriver{
				podConfigStore:     NewPodConfigStore(),
				cpuAllocationStore: NewCPUAllocationStore(topo, cpuset.New()),
				claimTracker:       store.NewClaimTracker(),
				cpuTopology:        topo,
			},
			runtimePods: []*api.PodSandbox{pod1, pod2},
			runtimeCtrs: []*api.Container{
				{Id: "p1-guaranteed", PodSandboxId: pod1.Id, Name: "guaranteed-ctr", Env: []string{fmt.Sprintf("%s_claim-A=%s", cdiEnvVarPrefix, "0,1")}},
				{Id: "p1-shared", PodSandboxId: pod1.Id, Name: "shared-ctr"},
				{Id: "p2-shared", PodSandboxId: pod2.Id, Name: "shared-ctr"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.driver.Synchronize(context.Background(), tc.runtimePods, tc.runtimeCtrs)

			if tc.expectedError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			// Verify that the store has been populated correctly
			require.Equal(t, len(tc.runtimePods), len(tc.driver.podConfigStore.configs))
			for _, pod := range tc.runtimePods {
				for _, container := range tc.runtimeCtrs {
					if container.PodSandboxId != pod.Id {
						continue
					}
					state := tc.driver.podConfigStore.GetContainerState(types.UID(pod.Uid), container.Name)
					require.NotNil(t, state)
				}
			}
		})
	}
}

func containerIDsFromUpdates(updates []*api.ContainerUpdate) []string {
	ids := make([]string, 0, len(updates))
	for _, upd := range updates {
		ids = append(ids, upd.ContainerId)
	}
	sort.Strings(ids)
	return ids
}
