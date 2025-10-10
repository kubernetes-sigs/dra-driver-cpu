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

	"github.com/containerd/nri/pkg/api"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/cpuset"
)

// Synchronize is called by the NRI to synchronize the state of the driver during bootstrap.
func (cp *CPUDriver) Synchronize(ctx context.Context, pods []*api.PodSandbox, containers []*api.Container) ([]*api.ContainerUpdate, error) {
	klog.Infof("Synchronized state with the runtime (%d pods, %d containers)...",
		len(pods), len(containers))

	cpuAllocationStore := NewCPUAllocationStore(cp.cpuInfoProvider, cp.reservedCPUs)
	podConfigStore := NewPodConfigStore()

	for _, pod := range pods {
		klog.Infof("Synchronize pod %s/%s UID %s", pod.Namespace, pod.Name, pod.Uid)
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
				state = NewContainerState(container.GetName(), containerUID)
			} else {
				allGuaranteedCPUs := cpuset.New()
				claimUIDs := []types.UID{}
				for uid, cpus := range claimAllocations {
					allGuaranteedCPUs = allGuaranteedCPUs.Union(cpus)
					claimUIDs = append(claimUIDs, uid)
					cpuAllocationStore.AddResourceClaimAllocation(uid, cpus)
				}
				klog.Infof("Synchronize: Found guaranteed CPUs for pod %s/%s container %s with cpus: %v", pod.Namespace, pod.Name, container.Name, allGuaranteedCPUs.String())
				state = NewContainerState(container.GetName(), containerUID, claimUIDs...)
			}
			podConfigStore.SetContainerState(types.UID(pod.GetUid()), state)
		}
	}
	cp.podConfigStore = podConfigStore
	cp.cpuAllocationStore = cpuAllocationStore
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
	sharedCPUs := cp.cpuAllocationStore.GetSharedCPUs()
	sharedCPUContainers := cp.podConfigStore.GetContainersWithSharedCPUs()
	klog.Infof("Updating CPU allocation to: %v for containers without guaranteed CPUs", sharedCPUs.String())
	for _, containerUID := range sharedCPUContainers {
		if containerUID == excludeID {
			// Skip the container being created as it is already covered in the container adjustment.
			continue
		}

		containerUpdate := &api.ContainerUpdate{
			ContainerId: string(containerUID),
		}
		containerUpdate.SetLinuxCPUSetCPUs(sharedCPUs.String())
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
		klog.Errorf("Error parsing DRA env for container %s in pod %s/%s: %v", ctr.Name, pod.Namespace, pod.Name, err)
	}

	containerId := types.UID(ctr.GetId())
	podUID := types.UID(pod.GetUid())

	if len(claimAllocations) == 0 {
		// This is a shared container.
		state := NewContainerState(ctr.GetName(), containerId)
		cp.podConfigStore.SetContainerState(podUID, state)

		sharedCPUs := cp.cpuAllocationStore.GetSharedCPUs()
		klog.Infof("No guaranteed CPUs found in DRA env for pod %s/%s container %s. Using shared CPUs %s", pod.Namespace, pod.Name, ctr.Name, sharedCPUs.String())
		adjust.SetLinuxCPUSetCPUs(sharedCPUs.String())
	} else {
		guaranteedCPUs := cpuset.New()
		claimUIDs := []types.UID{}
		for uid, cpus := range claimAllocations {
			guaranteedCPUs = guaranteedCPUs.Union(cpus)
			claimUIDs = append(claimUIDs, uid)
		}
		klog.Infof("Guaranteed CPUs found for pod:%s container:%s with cpus:%v", pod.Name, ctr.Name, guaranteedCPUs.String())
		state := NewContainerState(ctr.GetName(), containerId, claimUIDs...)
		adjust.SetLinuxCPUSetCPUs(guaranteedCPUs.String())
		cp.podConfigStore.SetContainerState(podUID, state)
		// Remove the guaranteed CPUs from the containers with shared CPUs.
		updates = cp.getSharedContainerUpdates(containerId)
	}

	return adjust, updates, nil
}

func (cp *CPUDriver) StopContainer(_ context.Context, pod *api.PodSandbox, ctr *api.Container) ([]*api.ContainerUpdate, error) {
	klog.Infof("StopContainer Pod:%s/%s PodUID:%s Container:%s ContainerID:%s", pod.Namespace, pod.Name, pod.Uid, ctr.Name, ctr.Id)
	updates := []*api.ContainerUpdate{}
	updateNeeded := cp.podConfigStore.RemoveContainerState(types.UID(pod.GetUid()), ctr.GetName())
	if updateNeeded {
		containerId := types.UID(ctr.GetId())
		// Remove the guaranteed CPUs from the containers with shared CPUs.
		updates = cp.getSharedContainerUpdates(containerId)
	}
	return updates, nil
}

// RemoveContainer handles container removal requests from the NRI.
func (cp *CPUDriver) RemoveContainer(_ context.Context, pod *api.PodSandbox, ctr *api.Container) error {
	klog.Infof("RemoveContainer Pod:%s/%s PodUID:%s Container:%s ContainerID:%s", pod.Namespace, pod.Name, pod.Uid, ctr.Name, ctr.Id)
	// this serves only for debugging purposes
	_ = cp.podConfigStore.RemoveContainerState(types.UID(pod.GetUid()), ctr.GetName())
	return nil
}
