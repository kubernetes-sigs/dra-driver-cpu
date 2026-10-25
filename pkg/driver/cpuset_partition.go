/*
Copyright The Kubernetes Authors.

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
	"maps"
	"slices"
	"time"

	"github.com/containerd/nri/pkg/api"
	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/ctxlog"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpupartition"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/cpuset"
)

// partitionRepairInterval bounds how long an invalid partition can go
// unnoticed without a kernel notification.
const partitionRepairInterval = 10 * time.Second

// exclusiveCPUs returns the CPUs of the claims CreateContainer recorded for
// the container, and false for a shared container.
func exclusiveCPUs(podUID types.UID, ctr *api.Container, pods *store.PodConfig, allocations *store.CPUAllocation, claims *store.ClaimTracker) (cpuset.CPUSet, bool, error) {
	state := pods.GetContainerState(podUID, ctr.GetName())
	if state == nil || !state.MatchesContainer(ctr.GetName(), types.UID(ctr.GetId())) {
		return cpuset.New(), false, fmt.Errorf("no CreateContainer state for container %s", ctr.GetId())
	}
	if !state.HasExclusiveCPUAllocation() {
		return cpuset.New(), false, nil
	}
	cpus := cpuset.New()
	for _, uid := range state.ClaimUIDs() {
		allocation, ok := allocations.GetResourceClaimAllocation(uid)
		if !ok || !claims.IsOwner(uid, podUID, ctr.GetName()) {
			return cpuset.New(), false, fmt.Errorf("container %s has no prepared claim %s", ctr.GetId(), uid)
		}
		cpus = cpus.Union(allocation)
	}
	return cpus, true, nil
}

// StartContainer is called after the runtime created the container cgroup
// and before the workload starts. Failing here keeps a container from running
// without its partition, so the runtime must require this plugin.
func (cp *CPUDriver) StartContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) error {
	if cp.partitions == nil {
		return nil
	}
	cp.stateMu.Lock()
	defer cp.stateMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	_, logger := ctxlog.WithValues(ctx, "opID", generateShortID(opIDLen), "pod", ctxlog.KObj(pod), "podUID", pod.Uid, "container", ctr.Name, "containerID", ctr.Id)
	cpus, exclusive, err := exclusiveCPUs(types.UID(pod.GetUid()), ctr, cp.podConfigStore, cp.cpuAllocationStore, cp.claimTracker)
	if err != nil || !exclusive {
		return err
	}
	if err := cp.partitions.Activate(ctr.GetId(), ctr.GetLinux().GetCgroupsPath(), cpus); err != nil {
		return fmt.Errorf("cpuset partition for container %s: %w", ctr.GetId(), err)
	}
	if err := ctx.Err(); err != nil {
		cp.releasePartition(logger, ctr)
		return err
	}
	logger.V(2).Info("activated cpuset partition", "cpus", cpus.String())
	return nil
}

// releasePartition does not fail the hook. An incomplete release keeps its
// CPUs, and the repair loop retries it.
func (cp *CPUDriver) releasePartition(logger logr.Logger, ctr *api.Container) {
	if cp.partitions == nil {
		return
	}
	if err := cp.partitions.Release(ctr.GetId()); err != nil {
		logger.Error(err, "cpuset partition release incomplete")
	}
}

// reconcilePartitions converges partitions to the running containers in the
// candidate stores. Created containers get theirs in StartContainer.
func (cp *CPUDriver) reconcilePartitions(logger logr.Logger, pods []*api.PodSandbox, containers []*api.Container, podConfig *store.PodConfig, allocations *store.CPUAllocation, claims *store.ClaimTracker) error {
	podUIDs := map[string]types.UID{}
	for _, pod := range pods {
		podUIDs[pod.GetId()] = types.UID(pod.GetUid())
	}
	expected := map[string]cpupartition.Lease{}
	for _, ctr := range containers {
		if ctr.GetState() != api.ContainerState_CONTAINER_RUNNING && ctr.GetState() != api.ContainerState_CONTAINER_PAUSED {
			continue
		}
		podUID, ok := podUIDs[ctr.GetPodSandboxId()]
		if !ok {
			continue
		}
		// Containers Synchronize did not trust get no partition.
		cpus, exclusive, err := exclusiveCPUs(podUID, ctr, podConfig, allocations, claims)
		if err != nil || !exclusive {
			continue
		}
		expected[ctr.GetId()] = cpupartition.Lease{CgroupsPath: ctr.GetLinux().GetCgroupsPath(), CPUs: cpus}
	}
	status, err := cp.partitions.Reconcile(expected)
	if err != nil {
		return fmt.Errorf("cpuset partitions: %w", err)
	}
	if status.Cleanup != nil {
		logger.Error(status.Cleanup, "removing stale cpuset partition state")
	}
	cp.recordPartitions(logger, status, nil)
	logger.Info("reconciled cpuset partitions", "valid", status.Valid, "invalid", status.Invalid, "releasing", status.Releasing)
	return nil
}

func (cp *CPUDriver) partitionRepairLoop(ctx context.Context) {
	logger := ctxlog.FromContext(ctx).WithName("cpuset-partitions")
	failing := map[string]bool{}
	for range cp.partitions.Watch(ctx, partitionRepairInterval) {
		cp.stateMu.Lock()
		status, err := cp.partitions.Repair()
		cp.stateMu.Unlock()
		if err != nil {
			logger.Error(err, "repairing cpuset partitions")
			continue
		}
		if status.Repaired > 0 {
			logger.Info("repaired cpuset partitions", "repaired", status.Repaired)
		}
		failing = cp.recordPartitions(logger, status, failing)
	}
}

// recordPartitions updates the metric and logs a container error only when
// the container starts failing.
func (cp *CPUDriver) recordPartitions(logger logr.Logger, status cpupartition.Status, failing map[string]bool) map[string]bool {
	cp.metrics.SetCPUSetPartitions(status.Valid, status.Invalid, status.Releasing)
	next := map[string]bool{}
	for _, id := range slices.Sorted(maps.Keys(status.Errors)) {
		if !failing[id] {
			logger.Error(status.Errors[id], "cpuset partition not valid", "containerID", id)
		}
		next[id] = true
	}
	return next
}
