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
	"encoding/json"
	"fmt"
	"io"
	"math"
	"reflect"
	"slices"
	"strings"

	"github.com/containerd/nri/pkg/api"
	"github.com/go-logr/logr"
	dracpuapi "github.com/kubernetes-sigs/dra-driver-cpu/api"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/memory"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/cpuset"
)

func hasResourceEnv(envs []string) bool {
	for _, env := range envs {
		if strings.HasPrefix(env, resourceEnvPrefix) {
			return true
		}
	}
	return false
}

func parseResourceEnv(envs []string) (map[string]requestAssignment, error) {
	result := make(map[string]requestAssignment)
	for _, env := range envs {
		if !strings.HasPrefix(env, resourceEnvPrefix) {
			continue
		}
		_, value, ok := strings.Cut(env, "=")
		if !ok {
			return nil, fmt.Errorf("malformed resource payload; do not set %s variables", resourceEnvPrefix)
		}
		var a requestAssignment
		decoder := json.NewDecoder(strings.NewReader(value))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&a); err != nil {
			return nil, fmt.Errorf("decode resource payload; use driver-injected CDI data: %w", err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, fmt.Errorf("resource payload has trailing data; use driver-injected CDI data")
		}
		if a.Version != 1 || a.ClaimUID == "" || a.Request == "" {
			return nil, fmt.Errorf("unsupported or incomplete resource payload; recreate the claim using this POC")
		}
		canonical, err := requestEnv(a)
		if err != nil {
			return nil, err
		}
		if canonical != env {
			return nil, fmt.Errorf("resource payload differs from canonical CDI data; do not set %s variables", resourceEnvPrefix)
		}
		name := requestDeviceName(a.ClaimUID, a.Request)
		if prev, ok := result[name]; ok && !reflect.DeepEqual(prev, a) {
			return nil, fmt.Errorf("conflicting resource payload for request %q; recreate the claim", a.Request)
		}
		result[name] = a
	}
	return result, nil
}

type composedResources struct {
	cpus  cpuset.CPUSet
	mems  cpuset.CPUSet
	bytes map[string]int64
	// Kubelet currently gives every claim consumer the whole claim's hugetlb budget.
	touchedHuge map[string]bool
	cpuClaims   []types.UID
}

func selectedResources(records map[types.UID]*composedClaim, selected map[string]requestAssignment, owner store.OwnerIdent) (composedResources, error) {
	out := composedResources{cpus: cpuset.New(), mems: cpuset.New(), bytes: make(map[string]int64), touchedHuge: make(map[string]bool)}
	for _, a := range selected {
		record := records[a.ClaimUID]
		if record == nil {
			return out, fmt.Errorf("claim %q is not prepared; retry after kubelet prepares the claim", a.ClaimUID)
		}
		prepared, ok := record.Requests[a.Request]
		if !ok || !reflect.DeepEqual(prepared, a) {
			return out, fmt.Errorf("claim %q request %q differs from prepared data; remove user-provided DRA_RESOURCE variables", a.ClaimUID, a.Request)
		}
		if prev := record.Owners[a.Request]; !prev.Vacant() && !prev.Equal(owner) {
			return out, fmt.Errorf("claim %q request %q belongs to container %q; use disjoint requests or separate claims", a.ClaimUID, a.Request, prev.ContainerName)
		}
		cpus, err := cpuset.Parse(a.CPUs)
		if err != nil {
			return out, fmt.Errorf("parse prepared CPU assignment: %w", err)
		}
		if !cpus.IsEmpty() {
			out.cpuClaims = append(out.cpuClaims, a.ClaimUID)
		}
		out.cpus = out.cpus.Union(cpus)
		for _, m := range a.Memory {
			if m.Bytes <= 0 || out.bytes[m.Kind] > math.MaxInt64-m.Bytes {
				return out, fmt.Errorf("invalid or overflowing %s budget; reduce the claim quantities", m.Kind)
			}
			out.bytes[m.Kind] += m.Bytes
			out.mems = out.mems.Union(cpuset.New(m.NUMA))
		}
		for _, request := range record.Requests {
			for _, m := range request.Memory {
				if m.Kind != memory.Regular {
					out.touchedHuge[m.Kind] = true
				}
			}
		}
	}
	return out, nil
}

type memoryAdjuster interface {
	SetLinuxCPUSetMems(string)
	SetLinuxMemoryLimit(int64)
	AddLinuxHugepageLimit(string, uint64)
}

func (r composedResources) apply(a memoryAdjuster) {
	if !r.mems.IsEmpty() {
		a.SetLinuxCPUSetMems(r.mems.String())
	}
	if r.bytes[memory.Regular] > 0 {
		a.SetLinuxMemoryLimit(r.bytes[memory.Regular])
	}
	if r.touchedHuge[memory.Huge2M] {
		a.AddLinuxHugepageLimit("2MB", uint64(r.bytes[memory.Huge2M])) //nolint:gosec // selectedResources bounds the sum to nonnegative int64.
	}
	if r.touchedHuge[memory.Huge1G] {
		a.AddLinuxHugepageLimit("1GB", uint64(r.bytes[memory.Huge1G])) //nolint:gosec // selectedResources bounds the sum to nonnegative int64.
	}
}

func bindRequests(records map[types.UID]*composedClaim, selected map[string]requestAssignment, owner store.OwnerIdent) {
	for _, a := range selected {
		records[a.ClaimUID].Owners[a.Request] = owner
	}
}

func (cp *CPUDriver) createComposedContainerLocked(logger logr.Logger, pod *api.PodSandbox, ctr *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
	selected, err := parseResourceEnv(ctr.Env)
	if err != nil {
		return nil, nil, err
	}
	if !cp.memoryEnabled {
		for _, a := range selected {
			if len(a.Memory) > 0 {
				return nil, nil, fmt.Errorf("memory support is disabled; use --enable-memory=true before creating memory-claim containers")
			}
		}
	}
	owner := store.OwnerIdent{PodUID: types.UID(pod.Uid), ContainerName: ctr.Name}
	resources, err := selectedResources(cp.composedClaims, selected, owner)
	if err != nil {
		return nil, nil, err
	}
	legacy, err := parseDRAEnvToClaimAllocations(logger, ctr.Env)
	if err != nil {
		return nil, nil, err
	}
	for uid, cpus := range legacy {
		if cp.composedClaims[uid] != nil {
			return nil, nil, fmt.Errorf("claim %q has both legacy and request payloads; use only driver-injected CDI data", uid)
		}
		resources.cpus = resources.cpus.Union(cpus)
		resources.cpuClaims = append(resources.cpuClaims, uid)
	}
	if resources.cpus.IsEmpty() && cp.allocationState.GetSharedCPUs().IsEmpty() && !cp.allocationState.GetPreparedCPUs().IsEmpty() {
		return nil, nil, fmt.Errorf("no shared CPUs available; leave a CPU for memory-only containers")
	}
	var updates []*api.ContainerUpdate
	if !resources.cpus.IsEmpty() {
		updates, err = sharedContainerUpdatesFor(logger, cp.allocationState, cp.podState, types.UID(ctr.Id))
		if err != nil {
			return nil, nil, err
		}
	}
	if len(legacy) > 0 {
		if _, err := cp.allocationState.Bind(logger, owner.PodUID, ctr.Name, legacy); err != nil {
			return nil, nil, err
		}
	}
	bindRequests(cp.composedClaims, selected, owner)
	cp.podState.SetContainerState(owner.PodUID, store.NewContainerState(ctr.Name, types.UID(ctr.Id), resources.cpuClaims...))
	adjust := &api.ContainerAdjustment{}
	resources.apply(adjust)
	if resources.cpus.IsEmpty() {
		adjust.SetLinuxCPUSetCPUs(cp.allocationState.GetSharedCPUs().String())
	} else {
		adjust.SetLinuxCPUSetCPUs(resources.cpus.String())
		adjust.AddEnv(dracpuapi.EnvVarExclusiveAssignedCPUSet, resources.cpus.String())
	}
	return adjust, updates, nil
}

func cloneComposedClaims(records map[types.UID]*composedClaim) map[types.UID]*composedClaim {
	out := make(map[types.UID]*composedClaim, len(records))
	for uid, r := range records {
		copy := &composedClaim{Requests: make(map[string]requestAssignment), Owners: make(map[string]store.OwnerIdent)}
		for k, v := range r.Requests {
			copy.Requests[k] = v
		}
		for k, v := range r.Owners {
			copy.Owners[k] = v
		}
		out[uid] = copy
	}
	return out
}

// synchronizeComposed runs against candidate stores so failed recovery cannot half-commit.
func (cp *CPUDriver) synchronizeComposed(logger logr.Logger, pods []*api.PodSandbox, containers []*api.Container, allocations *store.AllocationState, podState *store.PodState) (map[types.UID]*composedClaim, []*api.ContainerUpdate, error) {
	records := cloneComposedClaims(cp.composedClaims)
	var updates []*api.ContainerUpdate
	refreshed := false
	for _, ctr := range containers {
		if !hasResourceEnv(ctr.Env) {
			continue
		}
		idx := slices.IndexFunc(pods, func(p *api.PodSandbox) bool { return p.Id == ctr.PodSandboxId })
		if idx < 0 {
			continue
		}
		pod := pods[idx]
		selected, err := parseResourceEnv(ctr.Env)
		if err != nil {
			return nil, nil, err
		}
		for _, a := range selected {
			if records[a.ClaimUID] != nil {
				continue
			}
			if cp.hasSynchronized {
				return nil, nil, fmt.Errorf("runtime references unprepared claim %q; recreate the container", a.ClaimUID)
			}
			reader, ok := cp.cdiMgr.(interface {
				GetRequestPeers(string) ([]string, error)
			})
			if !ok {
				return nil, nil, fmt.Errorf("CDI manager cannot recover request specs; use the POC CDI manager")
			}
			if !refreshed {
				if err := cp.cdiMgr.Refresh(); err != nil {
					return nil, nil, err
				}
				refreshed = true
			}
			envs, err := reader.GetRequestPeers(requestDeviceName(a.ClaimUID, a.Request))
			if err != nil {
				return nil, nil, err
			}
			peers, err := parseResourceEnv(envs)
			if err != nil {
				return nil, nil, err
			}
			r := &composedClaim{Requests: make(map[string]requestAssignment), Owners: make(map[string]store.OwnerIdent)}
			allCPUs := cpuset.New()
			for _, peer := range peers {
				if peer.ClaimUID != a.ClaimUID {
					return nil, nil, fmt.Errorf("CDI spec mixes claim identities; recreate claim %q", a.ClaimUID)
				}
				cpus, err := cpuset.Parse(peer.CPUs)
				if err != nil {
					return nil, nil, fmt.Errorf("parse CDI cpuset: %w", err)
				}
				if !allCPUs.Intersection(cpus).IsEmpty() {
					return nil, nil, fmt.Errorf("CDI requests overlap CPUs; recreate claim %q", a.ClaimUID)
				}
				allCPUs = allCPUs.Union(cpus)
				for _, m := range peer.Memory {
					d, ok := cp.memoryDevices[m.Pool+"/"+m.Device]
					if !cp.memoryEnabled || !ok || d.Kind != m.Kind || d.NUMA != m.NUMA || m.Bytes <= 0 || m.Bytes > d.Bytes || m.Bytes%d.PageSize != 0 {
						return nil, nil, fmt.Errorf("CDI memory assignment is unavailable; restore the original inventory before restarting claim %q", a.ClaimUID)
					}
				}
				r.Requests[peer.Request] = peer
			}
			if !allCPUs.IsEmpty() {
				if err := allocations.Reserve(logger, a.ClaimUID, allCPUs, false); err != nil {
					return nil, nil, err
				}
			}
			records[a.ClaimUID] = r
		}
		owner := store.OwnerIdent{PodUID: types.UID(pod.Uid), ContainerName: ctr.Name}
		resources, err := selectedResources(records, selected, owner)
		if err != nil {
			return nil, nil, err
		}
		legacy, err := parseDRAEnvToClaimAllocations(logger, ctr.Env)
		if err != nil {
			return nil, nil, err
		}
		for uid := range legacy {
			// The existing CPU recovery pass has already validated these assignments.
			if allocations.IsOwner(uid, owner.PodUID, ctr.Name) {
				resources.cpus = resources.cpus.Union(legacy[uid])
				resources.cpuClaims = append(resources.cpuClaims, uid)
			}
		}
		bindRequests(records, selected, owner)
		podState.SetContainerState(owner.PodUID, store.NewContainerState(ctr.Name, types.UID(ctr.Id), resources.cpuClaims...))
		update := &api.ContainerUpdate{ContainerId: ctr.Id}
		resources.apply(update)
		if !resources.cpus.IsEmpty() {
			update.SetLinuxCPUSetCPUs(resources.cpus.String())
		}
		updates = append(updates, update)
	}
	return records, updates, nil
}
