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
	klog.Infof("Synchronized state with the runtime (%d pods, %d containers)...",
		len(pods), len(containers))

	newPodConfigStore := NewPodConfigStore(cp.cpuInfoProvider)

	for _, pod := range pods {
		klog.Infof("Synchronize pod %s/%s UID %s", pod.Namespace, pod.Name, pod.Uid)
		for _, container := range containers {
			if container.PodSandboxId != pod.Id {
				continue
			}
			guaranteedCPUs, err := parseDRAEnvToCPUSet(container.Env)
			if err != nil {
				klog.Errorf("Error parsing DRA env for container %s in pod %s/%s: %v", container.Name, pod.Namespace, pod.Name, err)
			}

			var state *ContainerCPUState
			if guaranteedCPUs.IsEmpty() {
				klog.Infof("Synchronize(): No guaranteed CPUs found in DRA env for pod %s/%s container %s", pod.Namespace, pod.Name, container.Name)
				state = NewContainerCPUState(CPUTypeShared, container.GetName(), types.UID(container.GetId()), cpuset.New())
			} else {
				klog.Infof("Synchronize(): Guaranteed CPUs found for pod %s/%s container %s with cpus: %v", pod.Namespace, pod.Name, container.Name, guaranteedCPUs.String())
				state = NewContainerCPUState(CPUTypeGuaranteed, container.GetName(), types.UID(container.GetId()), guaranteedCPUs)
			}
			newPodConfigStore.SetContainerState(types.UID(pod.GetUid()), state)
		}
	}
	cp.podConfigStore = newPodConfigStore
	return nil, nil
}

func parseDRAEnvToCPUSet(envs []string) (cpuset.CPUSet, error) {
	guaranteedCPUs := cpuset.New()
	for _, env := range envs {
		if !strings.HasPrefix(env, cdiEnvVarPrefix) {
			continue
		}
		klog.Infof("Parsing DRA env entry: %q", env)

		keyVal := strings.SplitN(env, "=", 2)
		if len(keyVal) != 2 {
			return cpuset.New(), fmt.Errorf("malformed DRA env entry %q, expected format: %s_key=value", env, cdiEnvVarPrefix)
		}
		cpuSetValue := keyVal[1]

		parsedSet, err := cpuset.Parse(cpuSetValue)
		if err != nil {
			return cpuset.New(), fmt.Errorf("failed to parse cpuset value %q from env %q: %w", cpuSetValue, env, err)
		}

		guaranteedCPUs = guaranteedCPUs.Union(parsedSet)
	}

	return guaranteedCPUs, nil
}

func (cp *CPUDriver) getSharedContainerUpdates(currentContainerID types.UID) []*api.ContainerUpdate {
	updates := []*api.ContainerUpdate{}
	publicCPUs := cp.podConfigStore.GetPublicCPUs()
	sharedCPUContainers := cp.podConfigStore.GetContainersWithSharedCPUs()
	klog.Infof("Updating CPU allocation to: %v for containers without guaranteed CPUs", publicCPUs.String())
	for _, containerUID := range sharedCPUContainers {
		if containerUID == currentContainerID {
			// Skip the current container as it is already coveed in container adjustment.
			continue
		}
		containerUpdate := &api.ContainerUpdate{
			ContainerId: string(containerUID),
		}
		containerUpdate.SetLinuxCPUSetCPUs(publicCPUs.String())
		updates = append(updates, containerUpdate)
	}
	return updates
}

// CreateContainer handles container creation requests from the NRI.
func (cp *CPUDriver) CreateContainer(_ context.Context, pod *api.PodSandbox, ctr *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
	klog.Infof("CreateContainer Pod:%s/%s PodUID:%s Container:%s ContainerID:%s", pod.Namespace, pod.Name, pod.Uid, ctr.Name, ctr.Id)
	adjust := &api.ContainerAdjustment{}
	updates := []*api.ContainerUpdate{}

	guaranteedCPUs, err := parseDRAEnvToCPUSet(ctr.Env)
	if err != nil {
		klog.Errorf("Error parsing DRA env for container %s in pod %s/%s: %v", ctr.Name, pod.Namespace, pod.Name, err)
	}

	var state *ContainerCPUState
	containerId := types.UID(ctr.GetId())
	if guaranteedCPUs.IsEmpty() {
		state = NewContainerCPUState(CPUTypeShared, ctr.GetName(), containerId, cpuset.New())
		publicCPUs := cp.podConfigStore.GetPublicCPUs()
		klog.Infof("No guaranteed CPUs found in DRA env for pod %s/%s container %s. Using public CPUs %s", pod.Namespace, pod.Name, ctr.Name, publicCPUs.String())
		adjust.SetLinuxCPUSetCPUs(publicCPUs.String())
		cp.podConfigStore.SetContainerState(types.UID(pod.GetUid()), state)
	} else {
		state = NewContainerCPUState(CPUTypeGuaranteed, ctr.GetName(), containerId, guaranteedCPUs)
		klog.Infof("Guaranteed CPUs found for pod:%s container:%s with cpus:%v", pod.Name, ctr.Name, guaranteedCPUs.String())
		adjust.SetLinuxCPUSetCPUs(guaranteedCPUs.String())
		cp.podConfigStore.SetContainerState(types.UID(pod.GetUid()), state)

		// Remove the guaranteed CPUs from the containers with shared CPUs.
		updates = cp.getSharedContainerUpdates(containerId)
		setSharedCPUUpdateRequired(false)
	}

	if isSharedCPUUpdateRequired() {
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
	// TODO(pravk03): If a container with guaranteed CPUs is removed, we need to update the CPU assignment of other containers with shared CPUs.
	// There isn't a way to do that in this call. It would only be updated the next time CreateContainer() gets called.
	if cp.podConfigStore.IsContainerGuaranteed(types.UID(pod.Uid), ctr.Name) {
		setSharedCPUUpdateRequired(true)
	}
	cp.podConfigStore.RemoveContainerState(types.UID(pod.GetUid()), ctr.GetName())
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
	// TODO(pravk03): If a pod with guaranteed CPUs is removed, we need to update the CPU assignment of other containers with shared CPUs.
	// There isn't a way to do that in this call. It would only be updated the next time CreateContainer() gets called.
	if cp.podConfigStore.IsPodGuaranteed(types.UID(pod.Uid)) {
		setSharedCPUUpdateRequired(true)
	}
	cp.podConfigStore.DeletePodState(types.UID(pod.Uid))
	return nil
}
