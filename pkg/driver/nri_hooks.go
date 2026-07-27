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
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/ctxlog"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/cpuset"
)

// Synchronize is called by the NRI to synchronize the state of the driver during bootstrap.
func (cp *CPUDriver) Synchronize(ctx context.Context, pods []*api.PodSandbox, containers []*api.Container) ([]*api.ContainerUpdate, error) {
	_, logger := ctxlog.WithValues(ctx, "opID", generateShortID(opIDLen))

	// this happens once at startup and it's critical enough that we always want to see it.
	logger.Info("begin: synchronize state with the runtime", "numPods", len(pods), "numContainers", len(containers))
	defer logger.Info("end: synchronize state with the runtime", "numPods", len(pods), "numContainers", len(containers))

	cpuAllocationStore := store.NewCPUAllocation(cp.topology.cpuTopology, cp.topology.reservedCPUs)
	podConfigStore := store.NewPodConfig()
	claimTracker := store.NewClaimTracker()
	var containerUpdates []*api.ContainerUpdate
	cdiCacheRefreshAttempted := false

	for _, pod := range pods {
		pLogger := logger.WithValues("pod", ctxlog.KObj(pod), "podUID", pod.Uid)
		pLogger.V(2).Info("synchronize pod")
		for _, container := range containers {
			if container.PodSandboxId != pod.Id {
				continue
			}
			cLogger := pLogger.WithValues("container", container.Name)

			claimAllocations, err := parseDRAEnvToClaimAllocations(cLogger, container.Env)
			if err != nil {
				cLogger.Error(err, "ignoring container with malformed DRA env during synchronize")
				continue
			}
			containerUID := types.UID(container.GetId())
			var claimUIDs []types.UID
			allGuaranteedCPUs := cpuset.New()
			for uid, cpus := range claimAllocations {
				caLogger := cLogger.WithValues("claimUID", uid)
				if !cdiCacheRefreshAttempted {
					err = cp.cdiMgr.Refresh()
					cdiCacheRefreshAttempted = true
					if err != nil {
						logger.Error(err, "failed to refresh CDI cache, continuing with available CDI devices")
					}
				}

				deviceName := getCDIDeviceName(uid)
				envs, err := cp.cdiMgr.GetDeviceEnv(deviceName)
				if err != nil {
					caLogger.Error(err, "ignoring claim not prepared by this driver during synchronize")
					continue
				}
				err = validateSynchronizedClaimAllocation(caLogger, uid, cpus, envs)
				if err != nil {
					caLogger.Error(err, "ignoring invalid claim allocation during synchronize")
					continue
				}
				err = claimTracker.SetOwner(caLogger, uid, types.UID(pod.Uid), container.Name)
				if err != nil {
					return nil, err
				}

				allGuaranteedCPUs = allGuaranteedCPUs.Union(cpus)
				claimUIDs = append(claimUIDs, uid)
				cpuAllocationStore.AddResourceClaimAllocation(caLogger, uid, cpus)
			}

			var state *store.ContainerState
			if len(claimUIDs) == 0 {
				state = store.NewContainerState(container.GetName(), containerUID)
			} else {
				cLogger.V(2).Info("found guaranteed CPUs", "cpus", allGuaranteedCPUs.String())
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
	cp.claimTracker = claimTracker
	cp.refreshAllocationMetrics()

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
		if after, ok := strings.CutPrefix(key, cdiEnvVarPrefix+"_"); ok {
			uidStr := after
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

func (cp *CPUDriver) validatePreparedClaimAllocation(uid types.UID, cpus cpuset.CPUSet) error {
	preparedCPUs, ok := cp.cpuAllocationStore.GetResourceClaimAllocation(uid)
	if !ok {
		return fmt.Errorf("claim %q is not prepared by this driver", uid)
	}
	if !preparedCPUs.Equals(cpus) {
		return fmt.Errorf("validation failed for claim %q: cpuset mismatch (expected %q, got %q)", uid, preparedCPUs.String(), cpus.String())
	}
	return nil
}

func validateSynchronizedClaimAllocation(logger logr.Logger, uid types.UID, cpus cpuset.CPUSet, envs []string) error {
	allocations, err := parseDRAEnvToClaimAllocations(logger, envs)
	if err != nil {
		return fmt.Errorf("failed to parse CDI env for claim %q: %w", uid, err)
	}

	preparedCPUs, ok := allocations[uid]
	if !ok {
		return fmt.Errorf("validation failed for claim %q: driver-owned CDI spec %q does not contain a matching DRA allocation", uid, getCDIDeviceName(uid))
	}
	if !preparedCPUs.Equals(cpus) {
		return fmt.Errorf("validation failed for claim %q during synchronize: cpuset mismatch (expected %q from CDI, got %q from runtime)", uid, preparedCPUs.String(), cpus.String())
	}
	return nil
}

func (cp *CPUDriver) getSharedContainerUpdates(logger logr.Logger, excludeID types.UID) []*api.ContainerUpdate {
	updates := []*api.ContainerUpdate{}
	sharedCPUs := cp.cpuAllocationStore.GetSharedCPUs()
	sharedCPUContainers := cp.podConfigStore.GetContainersWithSharedCPUs()
	logger.V(2).Info("updating CPU allocation for containers without guaranteed CPUs", "sharedCPUs", sharedCPUs.String())
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
	_, logger := ctxlog.WithValues(ctx, "opID", generateShortID(opIDLen), "pod", ctxlog.KObj(pod), "podUID", pod.Uid, "container", ctr.Name, "containerID", ctr.Id)
	logger.V(2).Info("begin: CreateContainer")
	defer logger.V(2).Info("end: CreateContainer")

	adjust := &api.ContainerAdjustment{}
	var updates []*api.ContainerUpdate

	claimAllocations, err := parseDRAEnvToClaimAllocations(logger, ctr.Env)
	if err != nil {
		logger.Error(err, "error parsing DRA env for container")
		return nil, nil, err
	}

	containerId := types.UID(ctr.GetId())
	podUID := types.UID(pod.GetUid())

	if len(claimAllocations) == 0 {
		// This is a shared container.
		state := store.NewContainerState(ctr.GetName(), containerId)
		cp.podConfigStore.SetContainerState(podUID, state)

		sharedCPUs := cp.cpuAllocationStore.GetSharedCPUs()
		logger.V(2).Info("no guaranteed CPUs found, using shared CPUs", "sharedCPUs", sharedCPUs.String())
		adjust.SetLinuxCPUSetCPUs(sharedCPUs.String())
	} else {
		// NRI invokes CreateContainer for all containers. Only trust DRA env
		// entries that match a claim prepared by this driver.
		guaranteedCPUs := cpuset.New()
		claimUIDs := []types.UID{}
		for uid, cpus := range claimAllocations {
			cLogger := logger.WithValues("claimUID", uid)
			if err := cp.validatePreparedClaimAllocation(uid, cpus); err != nil {
				return nil, nil, err
			}

			err := cp.claimTracker.SetOwner(cLogger, uid, types.UID(pod.Uid), ctr.Name)
			if err != nil {
				return nil, nil, err
			}

			guaranteedCPUs = guaranteedCPUs.Union(cpus)
			claimUIDs = append(claimUIDs, uid)
		}
		if err := cp.cpuAllocationStore.EnforceResourceClaims(claimAllocations); err != nil {
			return nil, nil, err
		}
		logger.V(2).Info("guaranteed CPUs found", "cpus", guaranteedCPUs.String())
		state := store.NewContainerState(ctr.GetName(), containerId, claimUIDs...)
		adjust.SetLinuxCPUSetCPUs(guaranteedCPUs.String())
		cp.podConfigStore.SetContainerState(podUID, state)
		// Remove the guaranteed CPUs from the containers with shared CPUs.
		updates = cp.getSharedContainerUpdates(logger, containerId)
	}

	return adjust, updates, nil
}

// StopContainer releases a guaranteed container's CPUs back into the shared pool.
//
// CPU-allocation lifetime across the DRA and NRI hooks:
//   - PrepareResourceClaims (DRA) records a pending reservation and writes the CDI spec carrying
//     that cpuset. Pending CPUs stay available to shared containers but not to new exclusive claims.
//   - CreateContainer (NRI) validates and enforces the prepared allocation, then shrinks the shared
//     pool.
//   - StopContainer (NRI, here) marks the allocation pending and immediately re-broadens surviving
//     shared containers. The prepared allocation and owner remain available for a restart.
//   - UnprepareResourceClaims (DRA) is the authoritative cleanup point for the allocation and owner.
//   - Synchronize (NRI, on restart) rebuilds the stores from the running containers' CDI env.
//
// This relies on a ResourceClaim being owned by exactly one container (claim sharing is
// unsupported), which is what makes pending shared use safe. Revisit before enabling claim sharing,
// preemption, or mixed shared/isolated allocation.
func (cp *CPUDriver) StopContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) ([]*api.ContainerUpdate, error) {
	_, logger := ctxlog.WithValues(ctx, "opID", generateShortID(opIDLen), "pod", ctxlog.KObj(pod), "podUID", pod.Uid, "container", ctr.Name, "containerID", ctr.Id)
	logger.V(2).Info("begin: StopContainer")
	defer logger.V(2).Info("end: StopContainer")

	updates := []*api.ContainerUpdate{}
	claimUIDs := cp.podConfigStore.RemoveContainerState(types.UID(pod.GetUid()), ctr.GetName())
	entries := "none"
	if len(claimUIDs) > 0 {
		// Stop enforcing the claim while retaining its prepared allocation for a restart.
		// TODO: assumes ResourceClaims are not shared across pods/containers; revisit if sharing lands.
		cp.cpuAllocationStore.MarkResourceClaimsPending(claimUIDs...)
		cp.refreshAllocationMetrics()
		// Remove the guaranteed CPUs from the containers with shared CPUs.
		updates = cp.getSharedContainerUpdates(logger, types.UID(ctr.GetId()))
		entries = fmt.Sprintf("%d entries", len(updates))
	}
	logger.V(2).Info("StopContainer updates needed", "entries", entries)
	return updates, nil
}

// RemoveContainer handles container removal requests from the NRI.
func (cp *CPUDriver) RemoveContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) error {
	_, logger := ctxlog.WithValues(ctx, "opID", generateShortID(opIDLen), "pod", ctxlog.KObj(pod), "podUID", pod.Uid, "container", ctr.Name, "containerID", ctr.Id)
	logger.V(2).Info("begin: RemoveContainer")
	defer logger.V(2).Info("end: RemoveContainer")

	claimUIDs := cp.podConfigStore.RemoveContainerState(types.UID(pod.GetUid()), ctr.GetName())
	if len(claimUIDs) > 0 {
		// this serves only for debugging purposes. We should never get here
		logger.Info("RemoveContainer spurious updates needed (unexpected, please file a bug)", "updates", cp.getSharedContainerUpdates(logger, types.UID(ctr.GetId())))
	}
	return nil
}
