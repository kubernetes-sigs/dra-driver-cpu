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
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/containerd/nri/pkg/api"
	"github.com/go-logr/logr"
	dracpuapi "github.com/kubernetes-sigs/dra-driver-cpu/api"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/ctxlog"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/runtimecompat"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/checkpoint"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/cpuset"
)

// Configure is called each time the NRI plugin connects to the runtime, with
// the runtime name and version. When forceImmediateSharedCPUReclaim is unset,
// it decides from them whether the runtime is safe for the unsolicited
// UpdateContainers call; deciding on every connection means a runtime that was
// rolled back under a running driver turns the feature off again.
func (cp *CPUDriver) Configure(ctx context.Context, config, runtime, version string) (api.EventMask, error) {
	logger := ctxlog.FromContext(ctx)
	logger.V(2).Info("NRI Configure", "runtime", runtime, "version", version)
	if cp.forceImmediateSharedCPUReclaim != nil {
		return 0, nil
	}

	supported, reason := runtimecompat.SupportsUnsolicitedUpdate(runtime, version)
	cp.stateMu.Lock()
	if supported {
		cp.reconcile = &reconcileState{containerUpdater: cp.nriPlugin}
	} else {
		cp.reconcile = nil
	}
	cp.stateMu.Unlock()
	logger.Info("immediate shared CPU reclaim auto-detected", "enabled", supported, "reason", reason)

	// 0 keeps the events the stub derives from the implemented handlers.
	return 0, nil
}

// Synchronize is called by the NRI to synchronize the state of the driver during bootstrap.
func (cp *CPUDriver) Synchronize(ctx context.Context, pods []*api.PodSandbox, containers []*api.Container) (rupdates []*api.ContainerUpdate, rerr error) {
	cp.stateMu.Lock()
	defer cp.stateMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	startTime := time.Now()
	_, logger := ctxlog.WithValues(ctx, "opID", generateShortID(opIDLen))
	// Recovery runs at startup and on reconnect; always log it.
	logger.Info("begin: synchronize state with the runtime", "numPods", len(pods), "numContainers", len(containers))
	defer logger.Info("end: synchronize state with the runtime", "numPods", len(pods), "numContainers", len(containers))

	defer func() { cp.metrics.RecordNRISynchronize(rerr, time.Since(startTime)) }()

	// Use a noop manager because we do a transactional full write once at the end of successful recovery.
	// If we use the real manager, we can end up with partial mutation of the real checkpoint data,
	// which effectively introduces silent data corruption.
	checkpointMgr := checkpoint.NewNOOPManager()
	// Runtime inventory is not the prepared-claim inventory: a claim may
	// have no container yet, or retain its owner between container restarts.
	// Only Unprepare releases these records while this process survives.
	allocationState := cp.allocationState.Clone()
	podState := store.NewPodState()
	// The prepared claims are known either from a previous Synchronize in this
	// process or from the checkpoint loaded at startup. Without either (first
	// start, or upgrade from a release without checkpoints) recover from the runtime env.
	trustPrepared := cp.hasSynchronized || cp.checkpointRestored
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
					state := cp.podState.GetContainerState(podUID, container.Name)
					if state != nil && state.MatchesContainer(container.Name, types.UID(container.GetId())) {
						podState.SetContainerState(podUID, state)
						cLogger.Error(err, "retaining known container state with malformed DRA env during synchronize")
						continue
					}
				}
				cLogger.Error(err, "ignoring container with malformed DRA env during synchronize")
				continue
			}
			containerUID := types.UID(container.GetId())
			allGuaranteedCPUs := cpuset.New()
			validatedClaimAllocations := make(map[store.ClaimRequest]cpuset.CPUSet)
			validatedClaimUIDs := sets.New[types.UID]()
			for claimReq, cpus := range claimAllocations {
				uid := claimReq.ClaimUID
				var prepared, owned bool
				if trustPrepared {
					_, prepared = cp.allocationState.GetResourceClaimAllocation(uid)
					owned = cp.allocationState.IsOwner(types.UID(pod.Uid), container.Name, claimReq)
				}
				caLogger := cLogger.WithValues("claimUID", uid, "request", claimReq.Request)
				if !cdiCacheRefreshAttempted {
					err = cp.cdiMgr.Refresh()
					cdiCacheRefreshAttempted = true
					if err != nil {
						logger.Error(err, "failed to refresh CDI cache, continuing with available CDI devices")
					}
				}

				envs, err := cp.cdiMgr.GetDeviceEnv(getCDISpecKey(uid))
				// Inconsistent claims are skipped, not fatal, so one container cannot
				// block recovery for every other container on the node.
				if err != nil {
					caLogger.Error(err, "ignoring claim without CDI state during synchronize", "prepared", prepared, "owned", owned)
					continue
				}
				claimRequests, err := validateSynchronizedClaimAllocation(caLogger, claimReq, cpus, envs)
				if err != nil {
					caLogger.Error(err, "ignoring invalid claim allocation during synchronize", "prepared", prepared, "owned", owned)
					continue
				}
				if trustPrepared && !prepared {
					// Unprepare is authoritative (the checkpoint carries it across restarts), so stale
					// runtime or CDI state cannot resurrect the claim.
					caLogger.Error(fmt.Errorf("runtime references claim %q without a prepared allocation", uid), "ignoring unprepared claim during synchronize")
					continue
				}
				// Synchronize restores an allocation that already exists in the runtime;
				// the shared-pool guard applies only to new reservations. Reserve the
				// full per-request split from the claim's CDI spec so sibling requests
				// of a multi-request claim stay reserved across containers.
				if err := allocationState.Reserve(caLogger, checkpointMgr, store.ClaimIdentifier{UID: uid}, claimRequests, false); err != nil {
					if !trustPrepared {
						return nil, err
					}
					// The runtime disagrees with the prepared allocation, which wins.
					caLogger.Error(err, "ignoring claim allocation that differs from the prepared one during synchronize")
					continue
				}

				allGuaranteedCPUs = allGuaranteedCPUs.Union(cpus)
				validatedClaimAllocations[claimReq] = cpus
				validatedClaimUIDs.Insert(uid)
			}

			if len(validatedClaimAllocations) > 0 {
				if _, err := allocationState.Bind(cLogger, checkpointMgr, types.UID(pod.Uid), container.Name, validatedClaimAllocations); err != nil {
					if !trustPrepared {
						return nil, err
					}
					// Bind is atomic, so the prepared owners are untouched; this container
					// is tracked as shared until the inconsistency is resolved.
					cLogger.Error(err, "ignoring claims bound to another container during synchronize")
					clear(validatedClaimAllocations)
					clear(validatedClaimUIDs)
				}
			}
			var state *store.ContainerState
			if len(validatedClaimAllocations) == 0 {
				state = store.NewContainerState(container.GetName(), containerUID)
			} else {
				cLogger.V(2).Info("found guaranteed CPUs", "cpus", allGuaranteedCPUs.String())
				state = store.NewContainerState(container.GetName(), containerUID, validatedClaimUIDs.UnsortedList()...)

				// Reconcile guaranteed container CPU mask.
				guaranteedUpdate := &api.ContainerUpdate{
					ContainerId: container.GetId(),
				}
				guaranteedUpdate.SetLinuxCPUSetCPUs(allGuaranteedCPUs.String())
				containerUpdates = append(containerUpdates, guaranteedUpdate)
			}
			podState.SetContainerState(types.UID(pod.GetUid()), state)
			cLogger.V(6).Info("set container state", "claims", validatedClaimUIDs.Len())
		}
	}

	// Reconcile container CPU masks to handle cases where the NRI plugin might have crashed
	// or restarted and missed updating the cgroup settings.
	// See: https://github.com/containerd/nri/issues/282
	sharedContainerUpdates, err := sharedContainerUpdatesFor(logger, allocationState, podState, types.UID(""))
	if err != nil {
		return nil, err
	}
	containerUpdates = append(containerUpdates, sharedContainerUpdates...)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := allocationState.StoreState(logger, cp.checkpointMgr); err != nil {
		return nil, err
	}
	cp.allocationState = allocationState
	cp.podState = podState
	if !cp.hasSynchronized && cp.initialSync != nil {
		close(cp.initialSync)
	}
	cp.hasSynchronized = true
	cp.refreshAllocationMetrics()
	logger.V(6).Info("synchronization complete", "updatesCount", len(containerUpdates))
	return containerUpdates, nil
}

// parseDRAEnvToClaimAllocations parses driver-injected DRA_CPUSET_<claimUID>[_<request>]=<cpuset>
// environment entries into per-request CPU allocations.
func parseDRAEnvToClaimAllocations(logger logr.Logger, envs []string) (map[store.ClaimRequest]cpuset.CPUSet, error) {
	const prefix = cdiEnvVarPrefix + "_"
	allocations := make(map[store.ClaimRequest]cpuset.CPUSet)
	for _, env := range envs {
		if !strings.HasPrefix(env, prefix) {
			continue
		}
		key, value, hasValue := strings.Cut(env, "=")
		if key == dracpuapi.EnvVarExclusiveAssignedCPUSet {
			continue
		}
		logger.V(4).Info("parsing DRA env entry", "env", env)
		if !hasValue {
			return nil, fmt.Errorf("malformed DRA env entry %q", env)
		}

		// Key format: DRA_CPUSET_<claimUID> (legacy) or DRA_CPUSET_<claimUID>_<request>.
		claimUID, req, hasReq := strings.Cut(strings.TrimPrefix(key, prefix), "_")
		if claimUID == "" || (hasReq && req == "") || strings.Contains(req, "_") {
			return nil, fmt.Errorf("malformed DRA env key %q", key)
		}

		parsedSet, err := cpuset.Parse(value)
		if err != nil {
			return nil, fmt.Errorf("failed to parse cpuset value %q from env %q: %w", value, env, err)
		}
		if parsedSet.IsEmpty() {
			return nil, fmt.Errorf("empty cpuset value in env %q", env)
		}

		claimReq := store.ClaimRequest{ClaimUID: types.UID(claimUID), Request: req}
		if existing, exists := allocations[claimReq]; exists && !existing.Equals(parsedSet) {
			return nil, fmt.Errorf("conflicting DRA env entries for claim %q request %q: %q vs %q", claimReq.ClaimUID, claimReq.Request, existing.String(), parsedSet.String())
		}
		allocations[claimReq] = parsedSet
	}

	for claimReq := range allocations {
		if claimReq.Request != "" {
			if _, hasLegacy := allocations[store.ClaimRequest{ClaimUID: claimReq.ClaimUID, Request: ""}]; hasLegacy {
				return nil, fmt.Errorf("conflicting legacy and per-request DRA env entries for claim %q", claimReq.ClaimUID)
			}
		}
	}

	return allocations, nil
}

func validateSynchronizedClaimAllocation(logger logr.Logger, claimReq store.ClaimRequest, cpus cpuset.CPUSet, envs []string) (map[string]cpuset.CPUSet, error) {
	allocations, err := parseDRAEnvToClaimAllocations(logger, envs)
	if err != nil {
		return nil, fmt.Errorf("failed to parse CDI env for claim %q: %w", claimReq.ClaimUID, err)
	}

	preparedCPUs, ok := allocations[claimReq]
	if !ok {
		return nil, fmt.Errorf("validation failed for claim %q request %q: driver-owned CDI spec %q does not contain a matching DRA allocation", claimReq.ClaimUID, claimReq.Request, getCDISpecKey(claimReq.ClaimUID))
	}
	if !preparedCPUs.Equals(cpus) {
		return nil, fmt.Errorf("validation failed for claim %q request %q during synchronize: cpuset mismatch (expected %q from CDI, got %q from runtime)", claimReq.ClaimUID, claimReq.Request, preparedCPUs.String(), cpus.String())
	}

	claimRequests := make(map[string]cpuset.CPUSet, len(allocations))
	for req, reqCPUs := range allocations {
		if req.ClaimUID != claimReq.ClaimUID {
			return nil, fmt.Errorf("validation failed for claim %q: driver-owned CDI spec %q contains unexpected claim %q", claimReq.ClaimUID, getCDISpecKey(claimReq.ClaimUID), req.ClaimUID)
		}
		claimRequests[req.Request] = reqCPUs
	}
	return claimRequests, nil
}

// sharedContainerUpdatesFor computes updates from the supplied stores. Callers
// using the live driver stores must hold stateMu; Synchronize passes candidate stores.
func sharedContainerUpdatesFor(logger logr.Logger, allocations *store.AllocationState, pods *store.PodState, excludeID types.UID) ([]*api.ContainerUpdate, error) {
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
	cp.stateMu.Lock()
	defer cp.stateMu.Unlock()
	if err := ctx.Err(); err != nil {
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
		return nil, nil, fmt.Errorf("failed to parse DRA environment for container %q in pod %q: %w", ctr.Name, pod.Uid, err)
	}
	claimCount = len(claimAllocations)
	containerId := types.UID(ctr.GetId())
	podUID := types.UID(pod.GetUid())

	if claimCount == 0 {
		// This is a shared container.
		sharedCPUs := cp.allocationState.GetSharedCPUs()
		if sharedCPUs.IsEmpty() && !cp.allocationState.GetPreparedCPUs().IsEmpty() {
			// NRI cannot represent an empty CPUSet as a ContainerAdjustment. Fail
			// closed instead of allowing the runtime to keep its default affinity.
			return nil, nil, fmt.Errorf("cannot create shared container %q in pod %q: no shared CPUs available", ctr.Name, pod.Uid)
		}
		state := store.NewContainerState(ctr.GetName(), containerId)
		cp.podState.SetContainerState(podUID, state)

		logger.V(2).Info("no guaranteed CPUs found, using shared CPUs", "sharedCPUs", sharedCPUs.String())
		adjust.SetLinuxCPUSetCPUs(sharedCPUs.String())
	} else {
		// NRI invokes CreateContainer for all containers. Only trust DRA env
		// entries that match a claim prepared by this driver.
		guaranteedCPUs := cpuset.New()
		claimUIDs := sets.New[types.UID]()
		for claimReq, cpus := range claimAllocations {
			guaranteedCPUs = guaranteedCPUs.Union(cpus)
			claimUIDs.Insert(claimReq.ClaimUID)
		}
		claimCount = claimUIDs.Len()
		updatedRequests, err := cp.allocationState.Bind(logger, cp.checkpointMgr, podUID, ctr.Name, claimAllocations)
		if err != nil {
			return nil, nil, fmt.Errorf("cannot bind claim to container %q in pod %q: %w", ctr.Name, pod.Uid, err)
		}
		logger.V(2).Info("guaranteed CPUs found", "cpus", guaranteedCPUs.String())
		state := store.NewContainerState(ctr.GetName(), containerId, claimUIDs.UnsortedList()...)
		adjust.SetLinuxCPUSetCPUs(guaranteedCPUs.String())
		adjust.AddEnv(dracpuapi.EnvVarExclusiveAssignedCPUSet, guaranteedCPUs.String())
		// An updated request means this is the first CreateContainer after Prepare, so
		// existing shared containers must be moved off the newly claimed CPUs.
		// On restart the owner already exists and no shared-container updates are
		// needed.
		if len(updatedRequests) > 0 {
			updates, err = sharedContainerUpdatesFor(logger, cp.allocationState, cp.podState, containerId)
			if err != nil {
				if unbindErr := cp.allocationState.Unbind(logger, cp.checkpointMgr, podUID, ctr.Name, updatedRequests...); unbindErr != nil {
					return nil, nil, errors.Join(
						fmt.Errorf("cannot update shared CPU pool after creating container %q in pod %q: %w", ctr.Name, pod.Uid, err),
						fmt.Errorf("cannot roll back claim bindings: %w", unbindErr),
					)
				}
				return nil, nil, fmt.Errorf("cannot update shared CPU pool after creating container %q in pod %q: %w", ctr.Name, pod.Uid, err)
			}
		}
		cp.podState.SetContainerState(podUID, state)
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
	cp.stateMu.Lock()
	defer cp.stateMu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	startTime := time.Now()

	_, logger := ctxlog.WithValues(ctx, "opID", generateShortID(opIDLen), "pod", ctxlog.KObj(pod), "podUID", pod.Uid, "container", ctr.Name, "containerID", ctr.Id)
	logger.V(2).Info("begin: StopContainer")
	defer logger.V(2).Info("end: StopContainer")

	updates := []*api.ContainerUpdate{}
	claimUIDs, removed := cp.podState.RemoveContainerState(types.UID(pod.GetUid()), ctr.GetName(), types.UID(ctr.GetId()))
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
	cp.stateMu.Lock()
	defer cp.stateMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}

	startTime := time.Now()

	_, logger := ctxlog.WithValues(ctx, "opID", generateShortID(opIDLen), "pod", ctxlog.KObj(pod), "podUID", pod.Uid, "container", ctr.Name, "containerID", ctr.Id)
	logger.V(2).Info("begin: RemoveContainer")
	defer logger.V(2).Info("end: RemoveContainer")

	claimUIDs, removed := cp.podState.RemoveContainerState(types.UID(pod.GetUid()), ctr.GetName(), types.UID(ctr.GetId()))
	if !removed {
		logger.V(2).Info("ignoring stale or unknown RemoveContainer event")
		return nil
	}
	if len(claimUIDs) > 0 {
		// this serves only for debugging purposes. We should never get here
		updates, err := sharedContainerUpdatesFor(logger, cp.allocationState, cp.podState, types.UID(ctr.GetId()))
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
