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
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/memory"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	"k8s.io/utils/cpuset"
	cdiparser "tags.cncf.io/container-device-interface/pkg/parser"
)

const resourceEnvPrefix = "DRA_RESOURCE_"

type memoryAssignment struct {
	Pool   string `json:"pool"`
	Device string `json:"device"`
	Kind   string `json:"kind"`
	NUMA   int    `json:"numa"`
	Bytes  int64  `json:"bytes"`
}

type requestAssignment struct {
	Version  int                `json:"version"`
	ClaimUID types.UID          `json:"claimUID"`
	Request  string             `json:"request"`
	CPUs     string             `json:"cpus,omitempty"`
	Memory   []memoryAssignment `json:"memory,omitempty"`
}

type composedClaim struct {
	Requests map[string]requestAssignment
	Owners   map[string]store.OwnerIdent
}

type requestCDIWriter interface {
	AddRequestDevices(logr.Logger, string, map[string]string) error
}

func requestDeviceName(uid types.UID, request string) string {
	sum := sha256.Sum256([]byte(request))
	return fmt.Sprintf("claim-%s-req-%x", uid, sum[:12])
}

func requestEnv(a requestAssignment) (string, error) {
	data, err := json.Marshal(a)
	if err != nil {
		return "", fmt.Errorf("encode request %q CDI payload: %w", a.Request, err)
	}
	sum := sha256.Sum256([]byte(string(a.ClaimUID) + "/" + a.Request))
	return fmt.Sprintf("%s%x=%s", resourceEnvPrefix, sum[:12], data), nil
}

func (cp *CPUDriver) addMemoryPools(resources *resourceslice.DriverResources) {
	byPool := make(map[string][]resourceapi.Device)
	for _, key := range slices.Sorted(maps.Keys(cp.memoryDevices)) {
		d := cp.memoryDevices[key]
		byPool[d.Pool(cp.nodeName)] = append(byPool[d.Pool(cp.nodeName)], d.Resource())
	}
	for pool, devices := range byPool {
		var chunks []resourceslice.Slice
		for chunk := range slices.Chunk(devices, cp.devicesPerResourceSlice) {
			chunks = append(chunks, resourceslice.Slice{Devices: chunk})
		}
		resources.Pools[pool] = resourceslice.Pool{Slices: chunks}
	}
}

func (cp *CPUDriver) classifyClaim(claim *resourceapi.ResourceClaim) (bool, error) {
	if claim.Status.Allocation == nil {
		return false, fmt.Errorf("claim %s/%s has no allocation; wait for scheduling", claim.Namespace, claim.Name)
	}
	requests := make(map[string]bool)
	composed := false
	for _, r := range claim.Status.Allocation.Devices.Results {
		if r.Driver != cp.driverName {
			continue
		}
		requests[r.Request] = true
		if _, ok := cp.memoryDevices[r.Pool+"/"+r.Device]; ok {
			composed = true
			continue
		}
		if strings.HasPrefix(r.Device, "memory-") || strings.HasPrefix(r.Device, "hugepages-") {
			return false, fmt.Errorf("memory device %s/%s is unavailable; enable memory and verify the node inventory", r.Pool, r.Device)
		}
		if cp.memoryEnabled {
			_, ok := getDeviceAttributes(cp.topology.deviceSlices, r.Device)
			if r.Pool != cp.nodeName || !ok {
				return false, fmt.Errorf("unknown device %s/%s; allocate from this node's published pools", r.Pool, r.Device)
			}
		}
	}
	return composed || (cp.memoryEnabled && len(requests) > 1), nil
}

func (cp *CPUDriver) prepareComposedClaimLocked(logger logr.Logger, claim *resourceapi.ResourceClaim) kubeletplugin.PrepareResult {
	if existing := cp.composedClaims[claim.UID]; existing != nil {
		return cp.writeComposedCDI(logger, claim, existing)
	}
	record := &composedClaim{Requests: make(map[string]requestAssignment), Owners: make(map[string]store.OwnerIdent)}
	assigned := cpuset.New()
	// CPU opaque-config and grouping checks must never inspect memory requests.
	cpuClaim := claim.DeepCopy()
	cpuClaim.Status.Allocation.Devices.Results = nil
	for _, r := range claim.Status.Allocation.Devices.Results {
		if r.Driver == cp.driverName && r.Pool == cp.nodeName {
			cpuClaim.Status.Allocation.Devices.Results = append(cpuClaim.Status.Allocation.Devices.Results, r)
		}
	}
	if cp.cpuDeviceMode == device.CPU_DEVICE_MODE_GROUPED && cp.cpuDeviceGroupBy == device.GROUP_BY_MACHINE {
		if err := checkDeviceRequestCountUpTo(cpuClaim, 1, cp.driverName); err != nil {
			return kubeletplugin.PrepareResult{Err: err}
		}
	}
	for _, r := range claim.Status.Allocation.Devices.Results {
		if r.Driver != cp.driverName {
			continue
		}
		if r.Request == "" {
			return kubeletplugin.PrepareResult{Err: fmt.Errorf("device %q has no selected request; correct the claim allocation", r.Device)}
		}
		a, ok := record.Requests[r.Request]
		if !ok {
			a = requestAssignment{Version: 1, ClaimUID: claim.UID, Request: r.Request}
		}
		if d, ok := cp.memoryDevices[r.Pool+"/"+r.Device]; ok {
			bytes, err := memory.Consumed(r, cp.driverName, d)
			if err != nil {
				return kubeletplugin.PrepareResult{Err: err}
			}
			a.Memory = append(a.Memory, memoryAssignment{Pool: r.Pool, Device: r.Device, Kind: d.Kind, NUMA: d.NUMA, Bytes: bytes})
		} else {
			var cur cpuset.CPUSet
			var err error
			if cp.cpuDeviceMode == device.CPU_DEVICE_MODE_GROUPED {
				cur, err = cp.allocateCPUResultLocked(logger, cpuClaim.Status.Allocation, r, assigned, cp.allocationState.GetSharedCPUs())
			} else {
				id, exists := cp.topology.NameToCPUID[r.Device]
				if !exists {
					return kubeletplugin.PrepareResult{Err: fmt.Errorf("unknown CPU device %q; use a published device", r.Device)}
				}
				cur = cpuset.New(id)
				if !cur.IsSubsetOf(cp.allocationState.GetSharedCPUs().Difference(assigned)) {
					err = fmt.Errorf("CPU device %q overlaps a prepared assignment; correct the allocation", r.Device)
				}
			}
			if err != nil {
				return kubeletplugin.PrepareResult{Err: err}
			}
			previous, err := cpuset.Parse(a.CPUs)
			if err != nil {
				return kubeletplugin.PrepareResult{Err: err}
			}
			a.CPUs = previous.Union(cur).String()
			assigned = assigned.Union(cur)
		}
		record.Requests[r.Request] = a
	}
	if !assigned.IsEmpty() {
		if err := cp.reserveResourceClaimAllocation(logger, claim.UID, assigned); err != nil {
			return kubeletplugin.PrepareResult{Err: err}
		}
	}
	result := cp.writeComposedCDI(logger, claim, record)
	if result.Err != nil {
		cp.allocationState.Release(logger, claim.UID)
		return result
	}
	if cp.composedClaims == nil {
		cp.composedClaims = make(map[types.UID]*composedClaim)
	}
	cp.composedClaims[claim.UID] = record
	if !assigned.IsEmpty() {
		cp.metrics.RecordClaimAllocatedCPUs(assigned.Size())
		cp.refreshAllocationMetrics()
	}
	return result
}

func (cp *CPUDriver) writeComposedCDI(logger logr.Logger, claim *resourceapi.ResourceClaim, record *composedClaim) kubeletplugin.PrepareResult {
	writer, ok := cp.cdiMgr.(requestCDIWriter)
	if !ok {
		return kubeletplugin.PrepareResult{Err: fmt.Errorf("CDI manager does not support request devices; use the POC CDI manager")}
	}
	envs := make(map[string]string)
	for req, a := range record.Requests {
		env, err := requestEnv(a)
		if err != nil {
			return kubeletplugin.PrepareResult{Err: err}
		}
		envs[requestDeviceName(claim.UID, req)] = env
	}
	if err := writer.AddRequestDevices(logger, getCDIDeviceName(claim.UID), envs); err != nil {
		return kubeletplugin.PrepareResult{Err: err}
	}
	var devices []kubeletplugin.Device
	for _, r := range claim.Status.Allocation.Devices.Results {
		if r.Driver != cp.driverName {
			continue
		}
		prepared := kubeletplugin.Device{PoolName: r.Pool, DeviceName: r.Device, Requests: []string{r.Request}, CDIDeviceIDs: []string{cdiparser.QualifiedName(cdiVendor, cdiClass, requestDeviceName(claim.UID, r.Request))}}
		attrs, _ := getDeviceAttributes(cp.topology.deviceSlices, r.Device)
		if d, ok := cp.memoryDevices[r.Pool+"/"+r.Device]; ok {
			attrs = d.Resource().Attributes
		}
		metadata := make(map[string]resourceapi.DeviceAttribute, len(attrs))
		for k, v := range attrs {
			metadata[string(k)] = v
		}
		prepared.Metadata = &kubeletplugin.DeviceMetadata{Attributes: metadata}
		devices = append(devices, prepared)
	}
	return kubeletplugin.PrepareResult{Devices: devices}
}
