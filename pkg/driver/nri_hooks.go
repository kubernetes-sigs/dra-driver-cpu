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
	"strings"
	"sync"

	"github.com/containerd/nri/pkg/api"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/cpuset"
)

var (
	sharedCPUUpdateMutex    sync.Mutex
	sharedCPUUpdateRequired bool
)

// Synchronize is called by the NRI to synchronize the state of the driver during bootstrap.
func (cp *CPUDriver) Synchronize(ctx context.Context, pods []*api.PodSandbox, containers []*api.Container) ([]*api.ContainerUpdate, error) {
	klog.Infof("Synchronizing state with the runtime (%d pods, %d containers)...", len(pods), len(containers))

	cp.cpuAllocationStore = NewCPUAllocationStore(cp.cpuInfoProvider)
	cp.podConfigStore = NewPodConfigStore()

	for _, pod := range pods {
		podUID := types.UID(pod.GetUid())
		for _, container := range containers {
			if container.PodSandboxId != pod.Id {
				continue
			}

			claimAllocations, err := parseDRAEnvToClaimAllocations(container.Env)
			if err != nil {
				klog.Errorf("Error parsing DRA env for container %s in pod %s/%s: %v", container.Name, pod.Namespace, pod.Name, err)
				continue
			}

			containerUID := types.UID(container.GetId())
			var state *ContainerState
			if len(claimAllocations) == 0 {
				state = NewContainerState(CPUTypeShared, container.GetName(), containerUID, nil)
			} else {
				allGuaranteedCPUs := cpuset.New()
				claimUIDs := []types.UID{}
				for uid, cpus := range claimAllocations {
					allGuaranteedCPUs = allGuaranteedCPUs.Union(cpus)
					claimUIDs = append(claimUIDs, uid)
					cp.cpuAllocationStore.RestoreResourceClaimAllocation(uid, cpus)
				}
				klog.Infof("Synchronize: Found guaranteed CPUs for pod %s/%s container %s with cpus: %v", pod.Namespace, pod.Name, container.Name, allGuaranteedCPUs.String())
				state = NewContainerState(CPUTypeGuaranteed, container.GetName(), containerUID, claimUIDs)
			}
			cp.podConfigStore.RestoreContainerState(podUID, state)
		}
	}

	return nil, nil
}

func parseDRAEnvToClaimAllocations(envs []string) (map[types.UID]cpuset.CPUSet, error) {
	allocations := make(map[types.UID]cpuset.CPUSet)
	for _, env := range envs {
		if !strings.HasPrefix(env, cdiEnvVarPrefix) {
			continue
		}
		klog.Infof("Parsing DRA env entry: %q", env)
		parts := strings.SplitN(env, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("malformed DRA env entry %q", env)
		}
		key, value := parts[0], parts[1]
		var claimUID types.UID
		if strings.HasPrefix(key, cdiEnvVarPrefix+"_") {
			uidStr := strings.TrimPrefix(key, cdiEnvVarPrefix+"_")
			claimUID = types.UID(uidStr)
		} else {
			continue
		}

		parsedSet, err := cpuset.Parse(value)
		if err != nil {
			return nil, fmt.Errorf("failed to parse cpuset value %q from env %q: %w", value, env, err)
		}
		allocations[claimUID] = parsedSet
	}

	return allocations, nil
}

func (cp *CPUDriver) getSharedContainerUpdates(excludeID types.UID) []*api.ContainerUpdate {
	updates := []*api.ContainerUpdate{}
	availableCPUs := cp.cpuAllocationStore.GetSharedCPUs()
	sharedCPUContainers := cp.podConfigStore.GetSharedCPUContainerUIDs()

	klog.Infof("Updating shared CPU containers with available CPUs: %s", availableCPUs.String())

	for _, containerUID := range sharedCPUContainers {
		// Skip the container being created as it is already covered in the container adjustment.
		if containerUID == excludeID {
			continue
		}

		containerUpdate := &api.ContainerUpdate{
			ContainerId: string(containerUID),
		}
		containerUpdate.SetLinuxCPUSetCPUs(availableCPUs.String())
		updates = append(updates, containerUpdate)
	}
	return updates
}

// CreateContainer handles container creation requests from the NRI.
func (cp *CPUDriver) CreateContainer(_ context.Context, pod *api.PodSandbox, ctr *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
	klog.Infof("CreateContainer Pod:%s/%s PodUID:%s Container:%s ContainerID:%s", pod.Namespace, pod.Name, pod.Uid, ctr.Name, ctr.Id)
	adjust := &api.ContainerAdjustment{}
	var updates []*api.ContainerUpdate

	claimAllocations, err := parseDRAEnvToClaimAllocations(ctr.Env)
	if err != nil {
		return nil, nil, fmt.Errorf("error parsing DRA env for container %s: %w", ctr.Name, err)
	}

	containerId := types.UID(ctr.GetId())
	podUID := types.UID(pod.GetUid())

	if len(claimAllocations) == 0 {
		// This is a shared container.
		state := NewContainerState(CPUTypeShared, ctr.GetName(), containerId, nil)
		cp.podConfigStore.SetContainerState(podUID, state)

		availableCPUs := cp.cpuAllocationStore.GetSharedCPUs()
		klog.Infof("Container %s is not managed by DRA, running with shared CPUs: %s", ctr.Name, availableCPUs.String())
		adjust.SetLinuxCPUSetCPUs(availableCPUs.String())

		if isSharedCPUUpdateRequired() {
			updates = cp.getSharedContainerUpdates(containerId)
			setSharedCPUUpdateRequired(false)
		}
	} else {
		// This is a guaranteed container.
		allGuaranteedCPUs := cpuset.New()
		claimUIDs := []types.UID{}
		for uid, cpus := range claimAllocations {
			allGuaranteedCPUs = allGuaranteedCPUs.Union(cpus)
			claimUIDs = append(claimUIDs, uid)
		}

		state := NewContainerState(CPUTypeGuaranteed, ctr.GetName(), containerId, claimUIDs)
		cp.podConfigStore.SetContainerState(podUID, state)

		adjust.SetLinuxCPUSetCPUs(allGuaranteedCPUs.String())
		klog.Infof("Guaranteed CPUs found for pod:%s container:%s with cpus:%v", pod.Name, ctr.Name, allGuaranteedCPUs.String())

		updates = cp.getSharedContainerUpdates(containerId)
		setSharedCPUUpdateRequired(false)
	}

	return adjust, updates, nil
}

func setSharedCPUUpdateRequired(required bool) {
	sharedCPUUpdateMutex.Lock()
	defer sharedCPUUpdateMutex.Unlock()
	sharedCPUUpdateRequired = required
}

func isSharedCPUUpdateRequired() bool {
	sharedCPUUpdateMutex.Lock()
	defer sharedCPUUpdateMutex.Unlock()
	return sharedCPUUpdateRequired
}

// RemoveContainer handles container removal requests from the NRI.
func (cp *CPUDriver) RemoveContainer(_ context.Context, pod *api.PodSandbox, ctr *api.Container) error {
	klog.Infof("RemoveContainer Pod:%s/%s PodUID:%s Container:%s ContainerID:%s", pod.Namespace, pod.Name, pod.Uid, ctr.Name, ctr.Id)
	podUID := types.UID(pod.GetUid())
	containerState := cp.podConfigStore.GetContainerState(podUID, ctr.GetName())

	cp.podConfigStore.RemoveContainerState(podUID, ctr.GetName())

	if containerState != nil && containerState.cpuType == CPUTypeGuaranteed {
		setSharedCPUUpdateRequired(true)
	}

	return nil
}

// RunPodSandbox handles pod sandbox creation requests from the NRI.
func (cp *CPUDriver) RunPodSandbox(_ context.Context, pod *api.PodSandbox) error {
	klog.Infof("RunPodSandbox Pod %s/%s UID %s", pod.Namespace, pod.Name, pod.Uid)
	return nil
}

// StopPodSandbox handles pod sandbox stop requests from the NRI.
func (cp *CPUDriver) StopPodSandbox(_ context.Context, pod *api.PodSandbox) error {
	klog.Infof("StopPodSandbox Pod %s/%s UID %s", pod.Namespace, pod.Name, pod.Uid)
	return nil
}

// RemovePodSandbox handles pod sandbox removal requests from the NRI.
func (cp *CPUDriver) RemovePodSandbox(_ context.Context, pod *api.PodSandbox) error {
	klog.Infof("RemovePodSandbox Pod %s/%s UID %s", pod.Namespace, pod.Name, pod.Uid)
	cp.podConfigStore.DeletePodState(types.UID(pod.Uid))
	return nil
}
