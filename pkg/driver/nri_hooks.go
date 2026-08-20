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
			validatedClaimAllocations := make(map[types.UID]cpuset.CPUSet)
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
				// Synchronize restores an allocation that already exists in the runtime;
				// the shared-pool guard applies only to new reservations.
				if err := cpuAllocationStore.ReserveResourceClaimAllocation(caLogger, uid, cpus, false); err != nil {
					return nil, err
				}

				allGuaranteedCPUs = allGuaranteedCPUs.Union(cpus)
				claimUIDs = append(claimUIDs, uid)
				validatedClaimAllocations[uid] = cpus
			}

			var state *store.ContainerState
			if len(claimUIDs) == 0 {
				state = store.NewContainerState(container.GetName(), containerUID)
			} else {
				if _, err := claimTracker.SetOwner(cLogger, types.UID(pod.Uid), container.Name, claimUIDs...); err != nil {
					return nil, err
				}
				if err := cpuAllocationStore.ValidateResourceClaimAllocations(validatedClaimAllocations); err != nil {
					return nil, err
				}
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
	sharedContainerUpdates, err := cp.getSharedContainerUpdates(logger, types.UID(""))
	if err != nil {
		return nil, err
	}
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

func (cp *CPUDriver) getSharedContainerUpdates(logger logr.Logger, excludeID types.UID) ([]*api.ContainerUpdate, error) {
	updates := []*api.ContainerUpdate{}
	sharedCPUs := cp.cpuAllocationStore.GetSharedCPUs()
	preparedCPUs := cp.cpuAllocationStore.GetPreparedCPUs()
	sharedCPUContainers := cp.podConfigStore.GetContainersWithSharedCPUs()
	// An empty CPUSet is serialized by NRI as Cpus="", which means "do not
	// change the current CPUSet" rather than "clear the CPUSet". Never emit
	// that update while a prepared DRA allocation has exhausted the pool and
	// shared containers still exist. An empty pool with no prepared allocation
	// is valid when the node has no driver-managed CPUs.
	if sharedCPUs.IsEmpty() && !preparedCPUs.IsEmpty() && len(sharedCPUContainers) > 0 {
		return nil, fmt.Errorf("cannot update shared containers: no shared CPUs available")
	}
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
	return updates, nil
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
		sharedCPUs := cp.cpuAllocationStore.GetSharedCPUs()
		if sharedCPUs.IsEmpty() && !cp.cpuAllocationStore.GetPreparedCPUs().IsEmpty() {
			// NRI cannot represent an empty CPUSet as a ContainerAdjustment. Fail
			// closed instead of allowing the runtime to keep its default affinity.
			return nil, nil, fmt.Errorf("cannot create shared container: no shared CPUs available")
		}
		state := store.NewContainerState(ctr.GetName(), containerId)
		cp.podConfigStore.SetContainerState(podUID, state)

		logger.V(2).Info("no guaranteed CPUs found, using shared CPUs", "sharedCPUs", sharedCPUs.String())
		adjust.SetLinuxCPUSetCPUs(sharedCPUs.String())
	} else {
		// NRI invokes CreateContainer for all containers. Only trust DRA env
		// entries that match a claim prepared by this driver.
		guaranteedCPUs := cpuset.New()
		claimUIDs := []types.UID{}
		for uid, cpus := range claimAllocations {
			guaranteedCPUs = guaranteedCPUs.Union(cpus)
			claimUIDs = append(claimUIDs, uid)
		}
		newOwners, err := cp.claimTracker.SetOwner(logger, podUID, ctr.Name, claimUIDs...)
		if err != nil {
			return nil, nil, err
		}
		if err := cp.cpuAllocationStore.ValidateResourceClaimAllocations(claimAllocations); err != nil {
			cp.claimTracker.Cleanup(newOwners...)
			return nil, nil, err
		}
		logger.V(2).Info("guaranteed CPUs found", "cpus", guaranteedCPUs.String())
		state := store.NewContainerState(ctr.GetName(), containerId, claimUIDs...)
		adjust.SetLinuxCPUSetCPUs(guaranteedCPUs.String())
		// A new owner means this is the first CreateContainer after Prepare, so
		// existing shared containers must be moved off the newly claimed CPUs.
		// On restart the owner already exists and no shared-container updates are
		// needed.
		if len(newOwners) > 0 {
			updates, err = cp.getSharedContainerUpdates(logger, containerId)
			if err != nil {
				cp.claimTracker.Cleanup(newOwners...)
				return nil, nil, err
			}
		}
		cp.podConfigStore.SetContainerState(podUID, state)
	}

	return adjust, updates, nil
}

// StopContainer removes runtime container state without changing DRA-owned allocations.
//
// CPU-allocation lifetime across the DRA and NRI hooks:
//   - PrepareResourceClaims (DRA) reserves CPUs and writes the CDI spec carrying that cpuset.
//   - CreateContainer (NRI) validates the CDI cpuset and applies it to the container.
//   - StopContainer (NRI, here) removes only the matching runtime container state. The prepared
//     allocation and owner remain unchanged so a restarted container reuses the same CPUs.
//   - UnprepareResourceClaims (DRA) is the authoritative release point for the allocation and owner.
//   - Synchronize (NRI, on restart) rebuilds the stores from the running containers' CDI env.
func (cp *CPUDriver) StopContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) ([]*api.ContainerUpdate, error) {
	_, logger := ctxlog.WithValues(ctx, "opID", generateShortID(opIDLen), "pod", ctxlog.KObj(pod), "podUID", pod.Uid, "container", ctr.Name, "containerID", ctr.Id)
	logger.V(2).Info("begin: StopContainer")
	defer logger.V(2).Info("end: StopContainer")

	updates := []*api.ContainerUpdate{}
	_, removed := cp.podConfigStore.RemoveContainerState(types.UID(pod.GetUid()), ctr.GetName(), types.UID(ctr.GetId()))
	if !removed {
		logger.V(2).Info("ignoring stale or unknown StopContainer event")
		return updates, nil
	}
	return updates, nil
}

// RemoveContainer handles container removal requests from the NRI.
func (cp *CPUDriver) RemoveContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) error {
	_, logger := ctxlog.WithValues(ctx, "opID", generateShortID(opIDLen), "pod", ctxlog.KObj(pod), "podUID", pod.Uid, "container", ctr.Name, "containerID", ctr.Id)
	logger.V(2).Info("begin: RemoveContainer")
	defer logger.V(2).Info("end: RemoveContainer")

	claimUIDs, removed := cp.podConfigStore.RemoveContainerState(types.UID(pod.GetUid()), ctr.GetName(), types.UID(ctr.GetId()))
	if !removed {
		logger.V(2).Info("ignoring stale or unknown RemoveContainer event")
		return nil
	}
	if len(claimUIDs) > 0 {
		// this serves only for debugging purposes. We should never get here
		updates, err := cp.getSharedContainerUpdates(logger, types.UID(ctr.GetId()))
		if err != nil {
			logger.Error(err, "unable to calculate shared container updates after RemoveContainer")
		} else {
			logger.Info("RemoveContainer spurious updates needed (unexpected, please file a bug)", "updates", updates)
		}
	}
	return nil
}
