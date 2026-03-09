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
	"slices"
	"sort"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpumanager"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/identifiers"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	"k8s.io/klog/v2"
	"k8s.io/utils/cpuset"
	"k8s.io/utils/ptr"
	cdiparser "tags.cncf.io/container-device-interface/pkg/parser"
)

const (
	// maxDevicesPerResourceSlice is the maximum number of devices that can be packed into a single
	// ResourceSlice object. This is a hard limit defined in the Kubernetes API at
	// https://github.com/kubernetes/kubernetes/blob/8e6d788887034b799f6c2a86991a68a080bb0576/pkg/apis/resource/types.go#L245
	maxDevicesPerResourceSlice = 128
	cpuDevicePrefix            = "cpudev"

	// Grouped Mode
	// cpuResourceQualifiedName is the qualified name for the CPU resource capacity.
	cpuResourceQualifiedName = identifiers.CPUResourceQualifiedNameKey

	cpuDeviceSocketGroupedPrefix = "cpudevsocket"
	cpuDeviceNUMAGroupedPrefix   = "cpudevnuma"
)

// createGroupedCPUDeviceSlices creates Device objects based on the CPU topology, grouped by a specific criteria.
func (cp *CPUDriver) createGroupedCPUDeviceSlices() [][]resourceapi.Device {
	klog.Info("Creating grouped CPU devices", "groupBy", cp.cpuDeviceGroupBy)
	var devices []resourceapi.Device

	topo := cp.cpuTopology
	smtEnabled := topo.SMTEnabled

	switch cp.cpuDeviceGroupBy {
	case GROUP_BY_SOCKET:
		socketIDs := topo.CPUDetails.Sockets().List()
		for _, socketIDInt := range socketIDs {
			socketID := int64(socketIDInt)
			deviceName := fmt.Sprintf("%s%03d", cpuDeviceSocketGroupedPrefix, socketIDInt)
			socketCPUSet := topo.CPUDetails.CPUsInSockets(socketIDInt)
			allocatableCPUs := socketCPUSet.Difference(cp.reservedCPUs)
			availableCPUsInSocket := int64(allocatableCPUs.Size())

			if allocatableCPUs.Size() == 0 {
				continue
			}

			deviceCapacity := map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
				cpuResourceQualifiedName: {Value: *resource.NewQuantity(availableCPUsInSocket, resource.DecimalSI)},
			}

			cp.deviceNameToSocketID[deviceName] = socketIDInt

			devices = append(devices, resourceapi.Device{
				Name: deviceName,
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					"dra.cpu/socketID":   {IntValue: &socketID},
					"dra.cpu/numCPUs":    {IntValue: &availableCPUsInSocket},
					"dra.cpu/smtEnabled": {BoolValue: &smtEnabled},
				},
				Capacity:                 deviceCapacity,
				AllowMultipleAllocations: ptr.To(true),
			})
		}
	case GROUP_BY_NUMA_NODE:
		numaNodeIDs := topo.CPUDetails.NUMANodes().List()
		for _, numaIDInt := range numaNodeIDs {
			numaID := int64(numaIDInt)
			deviceName := fmt.Sprintf("%s%03d", cpuDeviceNUMAGroupedPrefix, numaIDInt)
			numaNodeCPUSet := topo.CPUDetails.CPUsInNUMANodes(numaIDInt)
			allocatableCPUs := numaNodeCPUSet.Difference(cp.reservedCPUs)
			availableCPUsInNUMANode := int64(allocatableCPUs.Size())

			if allocatableCPUs.Size() == 0 {
				continue
			}

			// All CPUs in a NUMA node belong to the same socket.
			anyCPU := allocatableCPUs.UnsortedList()[0]
			socketID := int64(topo.CPUDetails[anyCPU].SocketID)

			deviceCapacity := map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
				cpuResourceQualifiedName: {Value: *resource.NewQuantity(availableCPUsInNUMANode, resource.DecimalSI)},
			}

			cp.deviceNameToNUMANodeID[deviceName] = numaIDInt

			devices = append(devices, resourceapi.Device{
				Name: deviceName,
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					"dra.cpu/numaNodeID": {IntValue: &numaID},
					"dra.cpu/socketID":   {IntValue: &socketID},
					"dra.cpu/numCPUs":    {IntValue: &availableCPUsInNUMANode},
					"dra.cpu/smtEnabled": {BoolValue: &smtEnabled},
					// TODO(pravk03): Remove. Hack to align with NIC (DRANet). We need some standard attribute to align other resources with CPU.
					"dra.net/numaNode": {IntValue: &numaID},
				},
				Capacity:                 deviceCapacity,
				AllowMultipleAllocations: ptr.To(true),
			})
		}
	}

	if len(devices) == 0 {
		return nil
	}
	return [][]resourceapi.Device{devices}
}

// CreateCPUDeviceSlices creates Device objects based on the CPU topology.
// It groups CPUs by physical core to assign consecutive device IDs to hyperthreads.
// This allows the DRA scheduler, which requests resources in contiguous blocks,
// to co-locate workloads on hyperthreads of the same core.
func (cp *CPUDriver) createCPUDeviceSlices() [][]resourceapi.Device {
	reservedCPUs := make(map[int]bool)
	for _, cpuID := range cp.reservedCPUs.List() {
		reservedCPUs[cpuID] = true
	}

	var availableCPUs []cpuinfo.CPUInfo
	topo := cp.cpuTopology
	allCPUs := make([]cpuinfo.CPUInfo, 0, len(topo.CPUDetails))
	for _, cpu := range topo.CPUDetails {
		allCPUs = append(allCPUs, cpu)
		if !reservedCPUs[cpu.CpuID] {
			availableCPUs = append(availableCPUs, cpu)
		}
	}
	sort.Slice(availableCPUs, func(i, j int) bool {
		return availableCPUs[i].CpuID < availableCPUs[j].CpuID
	})

	processedCpus := make(map[int]bool)
	var coreGroups [][]cpuinfo.CPUInfo
	cpuInfoMap := make(map[int]cpuinfo.CPUInfo)
	for _, info := range allCPUs {
		cpuInfoMap[info.CpuID] = info
	}

	for _, cpu := range availableCPUs {
		if processedCpus[cpu.CpuID] {
			continue
		}
		if cpu.SiblingCpuID == -1 || reservedCPUs[cpu.SiblingCpuID] {
			coreGroups = append(coreGroups, []cpuinfo.CPUInfo{cpu})
			processedCpus[cpu.CpuID] = true
		} else {
			coreGroups = append(coreGroups, []cpuinfo.CPUInfo{cpu, cpuInfoMap[cpu.SiblingCpuID]})
			processedCpus[cpu.CpuID] = true
			processedCpus[cpu.SiblingCpuID] = true
		}
	}

	sort.Slice(coreGroups, func(i, j int) bool {
		return coreGroups[i][0].CpuID < coreGroups[j][0].CpuID
	})

	devId := 0
	var allDevices []resourceapi.Device
	for _, group := range coreGroups {
		for _, cpu := range group {
			numaNode := int64(cpu.NUMANodeID)
			cacheL3ID := int64(cpu.UncoreCacheID)
			socketID := int64(cpu.SocketID)
			coreID := int64(cpu.CoreID)
			cpuID := int64(cpu.CpuID)
			coreType := cpu.CoreType.String()
			deviceName := fmt.Sprintf("%s%03d", cpuDevicePrefix, devId)
			devId++
			cp.deviceNameToCPUID[deviceName] = cpu.CpuID
			cpuDevice := resourceapi.Device{
				Name: deviceName,
				Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
					"dra.cpu/numaNodeID": {IntValue: &numaNode},
					"dra.cpu/cacheL3ID":  {IntValue: &cacheL3ID},
					"dra.cpu/coreType":   {StringValue: &coreType},
					"dra.cpu/socketID":   {IntValue: &socketID},
					"dra.cpu/coreID":     {IntValue: &coreID},
					"dra.cpu/cpuID":      {IntValue: &cpuID},
					// TODO(pravk03): Remove. Hack to align with NIC (DRANet). We need some standard attribute to align other resources with CPU.
					"dra.net/numaNode": {IntValue: &numaNode},
				},
				Capacity: make(map[resourceapi.QualifiedName]resourceapi.DeviceCapacity),
			}
			allDevices = append(allDevices, cpuDevice)
		}
	}

	if len(allDevices) == 0 {
		return nil
	}

	// Chunk devices into slices of at most maxDevicesPerResourceSlice
	return slices.Collect(slices.Chunk(allDevices, maxDevicesPerResourceSlice))
}

// PublishResources publishes ResourceSlice for CPU resources.
func (cp *CPUDriver) PublishResources(ctx context.Context) {
	klog.Infof("Publishing resources")

	var deviceChunks [][]resourceapi.Device
	if cp.cpuDeviceMode == CPU_DEVICE_MODE_GROUPED {
		deviceChunks = cp.createGroupedCPUDeviceSlices()
	} else {
		deviceChunks = cp.createCPUDeviceSlices()
	}

	if deviceChunks == nil {
		klog.Infof("No devices to publish or error occurred.")
		return
	}

	slices := make([]resourceslice.Slice, 0, len(deviceChunks))
	for _, chunk := range deviceChunks {
		slices = append(slices, resourceslice.Slice{Devices: chunk})
	}

	resources := resourceslice.DriverResources{
		Pools: map[string]resourceslice.Pool{
			// All slices are published under the same pool for this node.
			cp.nodeName: {Slices: slices},
		},
	}

	err := cp.draPlugin.PublishResources(ctx, resources)
	if err != nil {
		klog.Errorf("error publishing resources: %v", err)
	}
}

// PrepareResourceClaims is called by the kubelet to prepare a resource claim.
func (cp *CPUDriver) PrepareResourceClaims(ctx context.Context, claims []*resourceapi.ResourceClaim) (map[types.UID]kubeletplugin.PrepareResult, error) {
	klog.Infof("PrepareResourceClaims is called: number of claims: %d", len(claims))

	result := make(map[types.UID]kubeletplugin.PrepareResult)

	if len(claims) == 0 {
		return result, nil
	}

	for _, claim := range claims {
		if cp.cpuDeviceMode == CPU_DEVICE_MODE_GROUPED {
			klog.Infof("Claim %s/%s is for a grouped resource", claim.Namespace, claim.Name)
			result[claim.UID] = cp.prepareGroupedResourceClaim(ctx, claim)
		} else {
			klog.Infof("Claim %s/%s is for an individual resource", claim.Namespace, claim.Name)
			result[claim.UID] = cp.prepareResourceClaim(ctx, claim)
		}
	}
	return result, nil
}

func getCDIDeviceName(uid types.UID) string {
	return fmt.Sprintf("claim-%s", uid)
}

func (cp *CPUDriver) prepareGroupedResourceClaim(ctx context.Context, claim *resourceapi.ResourceClaim) kubeletplugin.PrepareResult {
	klog.Infof("prepareResourceClaim claim:%s/%s", claim.Namespace, claim.Name)

	if claim.Status.Allocation == nil {
		return kubeletplugin.PrepareResult{
			Err: fmt.Errorf("claim %s/%s has no allocation", claim.Namespace, claim.Name),
		}
	}

	var cpuAssignment cpuset.CPUSet
	for _, alloc := range claim.Status.Allocation.Devices.Results {
		claimCPUCount := int64(0)
		if alloc.Driver != cp.driverName {
			continue
		}
		if quantity, ok := alloc.ConsumedCapacity[cpuResourceQualifiedName]; ok {
			count := quantity.Value()
			claimCPUCount = count
			klog.Infof("Found request for %d CPUs in device %s for claim %s", count, alloc.Device, claim.Name)
		}

		topo := cp.cpuTopology

		var availableCPUsForDevice cpuset.CPUSet
		if cp.cpuDeviceGroupBy == GROUP_BY_SOCKET {
			socketID, ok := cp.deviceNameToSocketID[alloc.Device]
			if !ok {
				return kubeletplugin.PrepareResult{Err: fmt.Errorf("no valid socket ID found for device %s", alloc.Device)}
			}
			socketCPUs := topo.CPUDetails.CPUsInSockets(socketID)
			availableCPUsForDevice = cp.cpuAllocationStore.GetSharedCPUs().Intersection(socketCPUs)
			klog.Infof("Socket %d CPUs:%s available CPUs: %s", socketID, socketCPUs.String(), availableCPUsForDevice.String())
		} else { // numanode
			numaNodeID, ok := cp.deviceNameToNUMANodeID[alloc.Device]
			if !ok {
				return kubeletplugin.PrepareResult{Err: fmt.Errorf("no valid NUMA node ID found for device %s", alloc.Device)}
			}
			numaCPUs := topo.CPUDetails.CPUsInNUMANodes(numaNodeID)
			availableCPUsForDevice = cp.cpuAllocationStore.GetSharedCPUs().Intersection(numaCPUs)
			klog.Infof("NUMA node %d CPUs:%s available CPUs: %s", numaNodeID, numaCPUs.String(), availableCPUsForDevice.String())
		}

		logger := klog.FromContext(ctx)
		cur, err := cpumanager.TakeByTopologyNUMAPacked(logger, topo, availableCPUsForDevice, int(claimCPUCount), cpumanager.CPUSortingStrategyPacked, true)
		if err != nil {
			return kubeletplugin.PrepareResult{Err: err}
		}
		cpuAssignment = cpuAssignment.Union(cur)
		klog.Infof("CPU assignment for device %s: %s. All cpus assigned:%s", alloc.Device, cur.String(), cpuAssignment.String())
	}

	if cpuAssignment.Size() == 0 {
		klog.V(5).Infof("prepareResourceClaim claim:%s/%s has no CPU allocations for this driver", claim.Namespace, claim.Name)
		return kubeletplugin.PrepareResult{}
	}

	cp.cpuAllocationStore.AddResourceClaimAllocation(claim.UID, cpuAssignment)

	deviceName := getCDIDeviceName(claim.UID)
	envVar := fmt.Sprintf("%s_%s=%s", cdiEnvVarPrefix, claim.UID, cpuAssignment.String())
	if err := cp.cdiMgr.AddDevice(deviceName, envVar); err != nil {
		return kubeletplugin.PrepareResult{Err: err}
	}

	qualifiedName := cdiparser.QualifiedName(cdiVendor, cdiClass, deviceName)
	klog.Infof("prepareResourceClaim CDIDeviceName:%s envVar:%s qualifiedName:%v", deviceName, envVar, qualifiedName)
	preparedDevices := []kubeletplugin.Device{}
	for _, allocResult := range claim.Status.Allocation.Devices.Results {
		preparedDevice := kubeletplugin.Device{
			PoolName:     allocResult.Pool,
			DeviceName:   allocResult.Device,
			CDIDeviceIDs: []string{qualifiedName},
			Requests:     []string{allocResult.Request},
		}
		preparedDevices = append(preparedDevices, preparedDevice)
	}

	klog.Infof("prepareResourceClaim preparedDevices:%+v", preparedDevices)
	return kubeletplugin.PrepareResult{
		Devices: preparedDevices,
	}
}

func (cp *CPUDriver) prepareResourceClaim(_ context.Context, claim *resourceapi.ResourceClaim) kubeletplugin.PrepareResult {
	klog.Infof("prepareResourceClaim claim:%s/%s", claim.Namespace, claim.Name)

	if claim.Status.Allocation == nil {
		return kubeletplugin.PrepareResult{
			Err: fmt.Errorf("claim %s/%s has no allocation", claim.Namespace, claim.Name),
		}
	}

	claimCPUIDs := []int{}
	for _, alloc := range claim.Status.Allocation.Devices.Results {
		if alloc.Driver != cp.driverName {
			continue
		}
		cpuID, ok := cp.deviceNameToCPUID[alloc.Device]
		if !ok {
			return kubeletplugin.PrepareResult{
				Err: fmt.Errorf("device %q not found in device to CPU ID map", alloc.Device),
			}
		}
		claimCPUIDs = append(claimCPUIDs, cpuID)
	}

	if len(claimCPUIDs) == 0 {
		klog.V(5).Infof("prepareResourceClaim claim:%s/%s has no CPU allocations for this driver", claim.Namespace, claim.Name)
		return kubeletplugin.PrepareResult{}
	}

	claimCPUSet := cpuset.New(claimCPUIDs...)
	cp.cpuAllocationStore.AddResourceClaimAllocation(claim.UID, claimCPUSet)
	deviceName := getCDIDeviceName(claim.UID)
	envVar := fmt.Sprintf("%s_%s=%s", cdiEnvVarPrefix, claim.UID, claimCPUSet.String())
	if err := cp.cdiMgr.AddDevice(deviceName, envVar); err != nil {
		return kubeletplugin.PrepareResult{Err: err}
	}

	qualifiedName := cdiparser.QualifiedName(cdiVendor, cdiClass, deviceName)
	klog.Infof("prepareResourceClaim CDIDeviceName:%s envVar:%s qualifiedName:%v", deviceName, envVar, qualifiedName)
	preparedDevices := []kubeletplugin.Device{}
	for _, allocResult := range claim.Status.Allocation.Devices.Results {
		preparedDevice := kubeletplugin.Device{
			PoolName:     allocResult.Pool,
			DeviceName:   allocResult.Device,
			CDIDeviceIDs: []string{qualifiedName},
		}
		preparedDevices = append(preparedDevices, preparedDevice)
	}

	return kubeletplugin.PrepareResult{
		Devices: preparedDevices,
	}
}

// UnprepareResourceClaims is called by the kubelet to unprepare the resources for a claim.
func (cp *CPUDriver) UnprepareResourceClaims(ctx context.Context, claims []kubeletplugin.NamespacedObject) (map[types.UID]error, error) {
	klog.Infof("UnprepareResourceClaims is called: number of claims: %d", len(claims))

	result := make(map[types.UID]error)

	if len(claims) == 0 {
		return result, nil
	}

	for _, claim := range claims {
		klog.Infof("UnprepareResourceClaims claim:%+v", claim)
		err := cp.unprepareResourceClaim(ctx, claim)
		result[claim.UID] = err
		if err != nil {
			klog.Infof("error unpreparing resources for claim %s/%s : %v", claim.Namespace, claim.Name, err)
		}
	}
	return result, nil
}

func (cp *CPUDriver) unprepareResourceClaim(_ context.Context, claim kubeletplugin.NamespacedObject) error {
	cp.cpuAllocationStore.RemoveResourceClaimAllocation(claim.UID)
	// Remove the device from the CDI spec file using the manager.
	return cp.cdiMgr.RemoveDevice(getCDIDeviceName(claim.UID))
}

func (cp *CPUDriver) HandleError(_ context.Context, err error, msg string) {
	// TODO: Implement this function
	klog.Error("HandleError error:", err, "msg:", msg)
}
