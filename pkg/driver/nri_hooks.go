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
	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"k8s.io/utils/cpuset"
)

// Synchronize is called by the NRI to synchronize the state of the driver during bootstrap.
func (cp *CPUDriver) Synchronize(ctx context.Context, pods []*api.PodSandbox, containers []*api.Container) ([]*api.ContainerUpdate, error) {
	logger := klog.FromContext(ctx)

	// this happens once at startup and it's critical enough that we always want to see it.
	logger.Info("begin: synchronize state with the runtime", "numPods", len(pods), "numContainers", len(containers))
	defer logger.Info("end: synchronize state with the runtime", "numPods", len(pods), "numContainers", len(containers))

	cpuAllocationStore := store.NewCPUAllocation(cp.cpuTopology, cp.reservedCPUs)
	podConfigStore := store.NewPodConfig()
	var containerUpdates []*api.ContainerUpdate

	for _, pod := range pods {
		pLogger := logger.WithValues("pod", klog.KObj(pod), "podUID", pod.Uid)
		pLogger.Info("synchronize pod")
		for _, container := range containers {
			if container.PodSandboxId != pod.Id {
				continue
			}
			cLogger := pLogger.WithValues("container", container.Name)

			claimAllocations, err := parseDRAEnvToClaimAllocations(cLogger, container.Env)
			if err != nil {
				cLogger.Error(err, "error parsing DRA env for container")
				continue
			}
			containerUID := types.UID(container.GetId())
			var state *store.ContainerState
			var claimUIDs []types.UID
			if len(claimAllocations) == 0 {
				state = store.NewContainerState(container.GetName(), containerUID)
			} else {
				allGuaranteedCPUs := cpuset.New()
				for uid, cpus := range claimAllocations {
					caLogger := cLogger.WithValues("claimUID", uid)
					err := cp.claimTracker.SetOwner(caLogger, uid, types.UID(pod.Uid), container.Name)
					if err != nil {
						return nil, err
					}

					allGuaranteedCPUs = allGuaranteedCPUs.Union(cpus)
					claimUIDs = append(claimUIDs, uid)
					cpuAllocationStore.AddResourceClaimAllocation(caLogger, uid, cpus)
				}
				cLogger.Info("found guaranteed CPUs", "cpus", allGuaranteedCPUs.String())
				state = store.NewContainerState(container.GetName(), containerUID, claimUIDs...)

				// Reconcile guaranteed container CPU mask.
				guaranteedUpdate := &api.ContainerUpdate{
					ContainerId: container.GetId(),
				}
				guaranteedUpdate.SetLinuxCPUSetCPUs(allGuaranteedCPUs.String())
				containerUpdates = append(containerUpdates, guaranteedUpdate)
			}
			podConfigStore.SetContainerState(types.UID(pod.GetUid()), state)
		}
	}

	cp.podConfigStore = podConfigStore
	cp.cpuAllocationStore = cpuAllocationStore

	// Reconcile container CPU masks to handle cases where the NRI plugin might have crashed
	// or restarted and missed updating the cgroup settings.
	// See: https://github.com/containerd/nri/issues/282
	sharedContainerUpdates := cp.getSharedContainerUpdates(logger, types.UID(""))
	containerUpdates = append(containerUpdates, sharedContainerUpdates...)
	return containerUpdates, nil
}

func parseDRAEnvToClaimAllocations(logger logr.Logger, envs []string) (map[types.UID]cpuset.CPUSet, error) {
	allocations := make(map[types.UID]cpuset.CPUSet)
	for _, env := range envs {
		if !strings.HasPrefix(env, cdiEnvVarPrefix) {
			continue
		}
		logger.V(4).Info("parsing DRA env entry", "env", env)
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

func (cp *CPUDriver) getSharedContainerUpdates(logger logr.Logger, excludeID types.UID) []*api.ContainerUpdate {
	updates := []*api.ContainerUpdate{}
	sharedCPUs := cp.cpuAllocationStore.GetSharedCPUs()
	sharedCPUContainers := cp.podConfigStore.GetContainersWithSharedCPUs()
	logger.Info("updating CPU allocation for containers without guaranteed CPUs", "sharedCPUs", sharedCPUs.String())
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
func (cp *CPUDriver) CreateContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
	logger := klog.FromContext(ctx).WithValues("pod", klog.KObj(pod), "podUID", pod.Uid, "container", ctr.Name, "containerID", ctr.Id)
	// unnecessary now, but easy to miss in the future. Reset the context to a known-good state to be forward compatible
	_ = klog.NewContext(ctx, logger)

	logger.V(4).Info("begin: CreateContainer")
	defer logger.V(4).Info("end: CreateContainer")

	adjust := &api.ContainerAdjustment{}
	var updates []*api.ContainerUpdate

	claimAllocations, err := parseDRAEnvToClaimAllocations(logger, ctr.Env)
	if err != nil {
		logger.Error(err, "error parsing DRA env for container")
	}

	containerId := types.UID(ctr.GetId())
	podUID := types.UID(pod.GetUid())

	if len(claimAllocations) == 0 {
		// This is a shared container.
		state := store.NewContainerState(ctr.GetName(), containerId)
		cp.podConfigStore.SetContainerState(podUID, state)

		sharedCPUs := cp.cpuAllocationStore.GetSharedCPUs()
		logger.Info("no guaranteed CPUs found, using shared CPUs", "sharedCPUs", sharedCPUs.String())
		adjust.SetLinuxCPUSetCPUs(sharedCPUs.String())
	} else {
		guaranteedCPUs := cpuset.New()
		claimUIDs := []types.UID{}
		for uid, cpus := range claimAllocations {
			cLogger := logger.WithValues("claimUID", uid)
			err := cp.claimTracker.SetOwner(cLogger, uid, types.UID(pod.Uid), ctr.Name)
			if err != nil {
				return nil, nil, err
			}

			guaranteedCPUs = guaranteedCPUs.Union(cpus)
			claimUIDs = append(claimUIDs, uid)
		}
		logger.Info("guaranteed CPUs found", "cpus", guaranteedCPUs.String())
		state := store.NewContainerState(ctr.GetName(), containerId, claimUIDs...)
		adjust.SetLinuxCPUSetCPUs(guaranteedCPUs.String())
		cp.podConfigStore.SetContainerState(podUID, state)
		// Remove the guaranteed CPUs from the containers with shared CPUs.
		updates = cp.getSharedContainerUpdates(logger, containerId)
	}

	return adjust, updates, nil
}

func (cp *CPUDriver) StopContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) ([]*api.ContainerUpdate, error) {
	logger := klog.FromContext(ctx).WithValues("pod", klog.KObj(pod), "podUID", pod.Uid, "container", ctr.Name, "containerID", ctr.Id)
	// unnecessary now, but easy to miss in the future. Reset the context to a known-good state to be forward compatible
	_ = klog.NewContext(ctx, logger)

	logger.V(4).Info("begin: StopContainer")
	defer logger.V(4).Info("end: StopContainer")

	updates := []*api.ContainerUpdate{}
	claimUIDs := cp.podConfigStore.RemoveContainerState(types.UID(pod.GetUid()), ctr.GetName())
	entries := "none"
	if len(claimUIDs) > 0 {
		// This early release in StopContainer is a workaround for a lifecycle mismatch between DRA and NRI.
		// The proper place to release claim allocations is in the DRA UnprepareResourceClaims hook.
		// However, NRI only allows pushing CPU mask updates to other containers during container lifecycle events
		// (like StopContainer). If we wait until UnprepareResourceClaims to release the CPUs, we miss the opportunity
		// to update the shared pool of existing containers, leaving them on a restricted pool until a new
		// container event occurs.
		// TODO: This workaround assumes that ResourceClaims are NOT shared across pods/containers. If claim sharing
		// is supported in the future, this early release of CPUS will need an update.
		for _, claimUID := range claimUIDs {
			cLogger := logger.WithValues("claimUID", claimUID)
			cp.cpuAllocationStore.RemoveResourceClaimAllocation(cLogger, claimUID)
		}
		// Remove the guaranteed CPUs from the containers with shared CPUs.
		updates = cp.getSharedContainerUpdates(logger, types.UID(ctr.GetId()))
		cp.claimTracker.Cleanup(claimUIDs...)
		entries = fmt.Sprintf("%d entries", len(updates))
	}
	logger.Info("StopContainer updates needed", "entries", entries)
	return updates, nil
}

// RemoveContainer handles container removal requests from the NRI.
func (cp *CPUDriver) RemoveContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) error {
	logger := klog.FromContext(ctx).WithValues("pod", klog.KObj(pod), "podUID", pod.Uid, "container", ctr.Name, "containerID", ctr.Id)
	// unnecessary now, but easy to miss in the future. Reset the context to a known-good state to be forward compatible
	_ = klog.NewContext(ctx, logger)

	logger.V(4).Info("begin: RemoveContainer")
	defer logger.V(4).Info("end: RemoveContainer")

	claimUIDs := cp.podConfigStore.RemoveContainerState(types.UID(pod.GetUid()), ctr.GetName())
	if len(claimUIDs) > 0 {
		// this serves only for debugging purposes. We should never get here
		logger.Info("RemoveContainer spurious updates needed (unexpected, please file a bug)", "updates", cp.getSharedContainerUpdates(logger, types.UID(ctr.GetId())))
	}
	return nil
}
