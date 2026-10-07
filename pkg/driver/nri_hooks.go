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
	"time"

	"github.com/containerd/nri/pkg/api"
	"github.com/go-logr/logr"
	dracpuapi "github.com/kubernetes-sigs/dra-driver-cpu/api"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/ctxlog"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/cpuset"
)

// Synchronize is called by the NRI to synchronize the state of the driver during bootstrap.
func (cp *CPUDriver) Synchronize(ctx context.Context, pods []*api.PodSandbox, containers []*api.Container) (rupdates []*api.ContainerUpdate, rerr error) {
	return cp.synchronize(ctx, pods, containers, nil)
}

func (cp *CPUDriver) synchronize(ctx context.Context, pods []*api.PodSandbox, containers []*api.Container, attempt *nriAttempt) (rupdates []*api.ContainerUpdate, rerr error) {
	cp.stateMu.Lock()
	defer cp.stateMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if attempt != nil {
		cp.nri.mu.Lock()
		if cp.nri.current != attempt || attempt.closed {
			cp.nri.mu.Unlock()
			return nil, fmt.Errorf("NRI connection closed before synchronization")
		}
		attempt.ready = false
		cp.nri.notifyLocked()
		cp.nri.mu.Unlock()
	}
	startTime := time.Now()
	_, logger := ctxlog.WithValues(ctx, "opID", generateShortID(opIDLen))
	// Recovery runs at startup and on reconnect; always log it.
	logger.Info("begin: synchronize state with the runtime", "numPods", len(pods), "numContainers", len(containers))
	defer logger.Info("end: synchronize state with the runtime", "numPods", len(pods), "numContainers", len(containers))

	defer func() { cp.metrics.RecordNRISynchronize(rerr, time.Since(startTime)) }()

	// Runtime inventory is not the prepared-claim inventory: a claim may
	// have no container yet, or retain its owner between container restarts.
	// Only Unprepare releases these records while this process survives.
	cpuAllocationStore := cp.cpuAllocationStore.Clone()
	podConfigStore := store.NewPodConfig()
	claimTracker := cp.claimTracker.Clone()
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
				// Keep tracking a known runtime container on reconnect, especially
				// a shared container that must receive future pool updates. Never
				// infer allocations from malformed env or reuse a replaced ID.
				if cp.hasSynchronized {
					podUID := types.UID(pod.GetUid())
					state := cp.podConfigStore.GetContainerState(podUID, container.Name)
					if state != nil && state.MatchesContainer(container.Name, types.UID(container.GetId())) {
						podConfigStore.SetContainerState(podUID, state)
						cLogger.Error(err, "retaining known container state with malformed DRA env during synchronize")
						continue
					}
				}
				cLogger.Error(err, "ignoring container with malformed DRA env during synchronize")
				continue
			}
			containerUID := types.UID(container.GetId())
			var claimUIDs []types.UID
			allGuaranteedCPUs := cpuset.New()
			validatedClaimAllocations := make(map[types.UID]cpuset.CPUSet)
			for uid, cpus := range claimAllocations {
				_, prepared := cp.cpuAllocationStore.GetResourceClaimAllocation(uid)
				owned := cp.claimTracker.IsOwner(uid, types.UID(pod.Uid), container.Name)
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
					if cp.hasSynchronized && prepared && owned {
						return nil, fmt.Errorf("CDI state missing for prepared claim %q: %w", uid, err)
					}
					caLogger.Error(err, "ignoring claim not prepared by this driver during synchronize")
					continue
				}
				err = validateSynchronizedClaimAllocation(caLogger, uid, cpus, envs)
				if err != nil {
					if cp.hasSynchronized && prepared && owned {
						return nil, err
					}
					caLogger.Error(err, "ignoring invalid claim allocation during synchronize")
					continue
				}
				if cp.hasSynchronized && !prepared {
					return nil, fmt.Errorf("runtime references claim %q without a prepared allocation", uid)
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
			cLogger.V(6).Info("set container state", "claims", len(claimUIDs))
		}
	}

	// Reconcile container CPU masks to handle cases where the NRI plugin might have crashed
	// or restarted and missed updating the cgroup settings.
	// See: https://github.com/containerd/nri/issues/282
	sharedContainerUpdates, err := sharedContainerUpdatesFor(logger, cpuAllocationStore, podConfigStore, types.UID(""))
	if err != nil {
		return nil, err
	}
	containerUpdates = append(containerUpdates, sharedContainerUpdates...)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Serialize the final connection check with disconnect, without holding
	// the lifecycle lock during CDI I/O. Old attempts must never publish state.
	if attempt != nil {
		cp.nri.mu.Lock()
		defer cp.nri.mu.Unlock()
		if cp.nri.current != attempt || attempt.closed {
			return nil, fmt.Errorf("NRI connection closed during synchronization")
		}
	}
	cp.podConfigStore = podConfigStore
	cp.cpuAllocationStore = cpuAllocationStore
	cp.claimTracker = claimTracker
	cp.hasSynchronized = true
	if attempt != nil {
		attempt.ready = true
		cp.nri.notifyLocked()
	}
	cp.refreshAllocationMetrics()
	logger.V(6).Info("synchronization complete", "updatesCount", len(containerUpdates))
	return containerUpdates, nil
}

func parseDRAEnvToClaimAllocations(logger logr.Logger, envs []string) (map[types.UID]cpuset.CPUSet, error) {
	allocations := make(map[types.UID]cpuset.CPUSet)
	for _, env := range envs {
		key, value, hasValue := strings.Cut(env, "=")
		// We won't have a conflict by constructions with the vars we inject from DRA side
		// and consume from NRI side, but still we add a defensive check.
		// So this is the explicit skip for well-known public environment variable.
		if key == dracpuapi.EnvVarExclusiveAssignedCPUSet {
			continue
		}
		if !strings.HasPrefix(key, cdiEnvVarPrefix) {
			continue
		}
		logger.V(4).Info("parsing DRA env entry", "env", env)
		if !hasValue {
			return nil, fmt.Errorf("malformed DRA env entry %q", env)
		}
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

// getSharedContainerUpdates requires stateMu. Synchronize uses its candidate stores instead.
func (cp *CPUDriver) getSharedContainerUpdates(logger logr.Logger, excludeID types.UID) ([]*api.ContainerUpdate, error) {
	return sharedContainerUpdatesFor(logger, cp.cpuAllocationStore, cp.podConfigStore, excludeID)
}

func sharedContainerUpdatesFor(logger logr.Logger, allocations *store.CPUAllocation, pods *store.PodConfig, excludeID types.UID) ([]*api.ContainerUpdate, error) {
	updates := []*api.ContainerUpdate{}
	sharedCPUs := allocations.GetSharedCPUs()
	preparedCPUs := allocations.GetPreparedCPUs()
	sharedCPUContainers := pods.GetContainersWithSharedCPUs()
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
func (cp *CPUDriver) CreateContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) (radjust *api.ContainerAdjustment, rupdates []*api.ContainerUpdate, rerr error) {
	return cp.createContainer(ctx, pod, ctr, nil)
}

func (cp *CPUDriver) createContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container, attempt *nriAttempt) (radjust *api.ContainerAdjustment, rupdates []*api.ContainerUpdate, rerr error) {
	cp.stateMu.Lock()
	defer cp.stateMu.Unlock()
	if err := cp.checkNRIRequest(ctx, attempt); err != nil {
		return nil, nil, err
	}

	startTime := time.Now()

	_, logger := ctxlog.WithValues(ctx, "opID", generateShortID(opIDLen), "pod", ctxlog.KObj(pod), "podUID", pod.Uid, "container", ctr.Name, "containerID", ctr.Id)
	logger.V(2).Info("begin: CreateContainer")
	defer logger.V(2).Info("end: CreateContainer")

	adjust := &api.ContainerAdjustment{}
	var updates []*api.ContainerUpdate

	claimCount := -1
	claimAllocations, err := parseDRAEnvToClaimAllocations(logger, ctr.Env)
	defer func() { cp.metrics.RecordNRICreateContainer(rerr, claimCount, time.Since(startTime)) }()
	if err != nil {
		logger.Error(err, "error parsing DRA env for container")
		return nil, nil, err
	}
	claimCount = len(claimAllocations)

	containerId := types.UID(ctr.GetId())
	podUID := types.UID(pod.GetUid())

	if claimCount == 0 {
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
		adjust.AddEnv(dracpuapi.EnvVarExclusiveAssignedCPUSet, guaranteedCPUs.String())
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
//   - Synchronize rebuilds runtime container state, retaining prepared claims and owners
//     while this process survives. Cold recovery uses running containers and CDI.
func (cp *CPUDriver) StopContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) ([]*api.ContainerUpdate, error) {
	return cp.stopContainer(ctx, pod, ctr, nil)
}

func (cp *CPUDriver) stopContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container, attempt *nriAttempt) ([]*api.ContainerUpdate, error) {
	cp.stateMu.Lock()
	defer cp.stateMu.Unlock()
	if err := cp.checkNRIRequest(ctx, attempt); err != nil {
		return nil, err
	}

	startTime := time.Now()

	_, logger := ctxlog.WithValues(ctx, "opID", generateShortID(opIDLen), "pod", ctxlog.KObj(pod), "podUID", pod.Uid, "container", ctr.Name, "containerID", ctr.Id)
	logger.V(2).Info("begin: StopContainer")
	defer logger.V(2).Info("end: StopContainer")

	updates := []*api.ContainerUpdate{}
	claimUIDs, removed := cp.podConfigStore.RemoveContainerState(types.UID(pod.GetUid()), ctr.GetName(), types.UID(ctr.GetId()))
	if !removed {
		logger.V(2).Info("ignoring stale or unknown StopContainer event")
		return updates, nil
	}

	// leverage the fact the hook can't fail in our current design
	cp.metrics.RecordNRIStopContainer(nil, len(claimUIDs), time.Since(startTime))
	return updates, nil
}

// RemoveContainer handles container removal requests from the NRI.
func (cp *CPUDriver) RemoveContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) error {
	return cp.removeContainer(ctx, pod, ctr, nil)
}

func (cp *CPUDriver) removeContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container, attempt *nriAttempt) error {
	cp.stateMu.Lock()
	defer cp.stateMu.Unlock()
	if err := cp.checkNRIRequest(ctx, attempt); err != nil {
		return err
	}

	startTime := time.Now()

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

	// leverage the fact the hook can't fail in our current design
	// **NOTE** because of the flow (see comment before StopContainer) this metric becomes a signal
	// we leaked state and StopContainer didn't clean up properly.
	cp.metrics.RecordNRIRemoveContainer(nil, len(claimUIDs), time.Since(startTime))
	return nil
}
