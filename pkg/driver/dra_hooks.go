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
	"os"
	"slices"
	"sort"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/ctxlog"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpumanager"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	"k8s.io/utils/cpuset"
	"k8s.io/utils/ptr"
	cdiparser "tags.cncf.io/container-device-interface/pkg/parser"
)

const (
	cpuDevicePrefix = "cpudev"

	// Grouped Mode
	// cpuResourceQualifiedName is the qualified name for the CPU resource capacity.
	cpuResourceQualifiedName = "dra.cpu/cpu"

	cpuDeviceSocketGroupedPrefix = "cpudevsocket"
	cpuDeviceNUMAGroupedPrefix   = "cpudevnuma"
)

type groupedCPUDeviceInfo struct {
	name       string
	cpus       cpuset.CPUSet
	socketID   int
	numaNodeID int
}

type cpuDeviceInfo struct {
	name string
	cpu  cpuinfo.CPUInfo
}

func (cp *CPUDriver) groupedCPUDeviceInfos() []groupedCPUDeviceInfo {
	var devices []groupedCPUDeviceInfo
	topo := cp.cpuTopology

	switch cp.cpuDeviceGroupBy {
	case GROUP_BY_SOCKET:
		socketIDs := topo.CPUDetails.Sockets().List()
		for _, socketID := range socketIDs {
			allocatableCPUs := topo.CPUDetails.CPUsInSockets(socketID).Difference(cp.reservedCPUs)
			if allocatableCPUs.Size() == 0 {
				continue
			}
			devices = append(devices, groupedCPUDeviceInfo{
				name:     fmt.Sprintf("%s%03d", cpuDeviceSocketGroupedPrefix, socketID),
				cpus:     allocatableCPUs,
				socketID: socketID,
			})
		}
	case GROUP_BY_NUMA_NODE:
		numaNodeIDs := topo.CPUDetails.NUMANodes().List()
		for _, numaID := range numaNodeIDs {
			allocatableCPUs := topo.CPUDetails.CPUsInNUMANodes(numaID).Difference(cp.reservedCPUs)
			if allocatableCPUs.Size() == 0 {
				continue
			}

			// All CPUs in a NUMA node belong to the same socket.
			anyCPU := allocatableCPUs.UnsortedList()[0]
			devices = append(devices, groupedCPUDeviceInfo{
				name:       fmt.Sprintf("%s%03d", cpuDeviceNUMAGroupedPrefix, numaID),
				cpus:       allocatableCPUs,
				socketID:   topo.CPUDetails[anyCPU].SocketID,
				numaNodeID: numaID,
			})
		}
	}
	return devices
}

// cpuDeviceInfos returns the stable individual CPU device enumeration used by
// both ResourceSlice publication and PrepareResourceClaims device lookup.
// Keep the ordering in one place so device names resolve to the same CPUs even
// when Prepare runs before the first ResourceSlice publication after restart.
func (cp *CPUDriver) cpuDeviceInfos() []cpuDeviceInfo {
	reservedCPUs := make(map[int]bool)
	for _, cpuID := range cp.reservedCPUs.List() {
		reservedCPUs[cpuID] = true
	}

	topo := cp.cpuTopology
	allCPUs := make([]cpuinfo.CPUInfo, 0, len(topo.CPUDetails))
	availableCPUs := []cpuinfo.CPUInfo{}
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
	coreGroups := [][]cpuinfo.CPUInfo{}
	cpuInfoMap := make(map[int]cpuinfo.CPUInfo)
	for _, info := range allCPUs {
		cpuInfoMap[info.CpuID] = info
	}

	for _, cpu := range availableCPUs {
		if processedCpus[cpu.CpuID] {
			continue
		}
		if cpu.SiblingCPUID == -1 || reservedCPUs[cpu.SiblingCPUID] {
			coreGroups = append(coreGroups, []cpuinfo.CPUInfo{cpu})
			processedCpus[cpu.CpuID] = true
		} else {
			coreGroups = append(coreGroups, []cpuinfo.CPUInfo{cpu, cpuInfoMap[cpu.SiblingCPUID]})
			processedCpus[cpu.CpuID] = true
			processedCpus[cpu.SiblingCPUID] = true
		}
	}

	sort.Slice(coreGroups, func(i, j int) bool {
		return coreGroups[i][0].CpuID < coreGroups[j][0].CpuID
	})

	devices := []cpuDeviceInfo{}
	devID := 0
	for _, group := range coreGroups {
		for _, cpu := range group {
			devices = append(devices, cpuDeviceInfo{
				name: fmt.Sprintf("%s%03d", cpuDevicePrefix, devID),
				cpu:  cpu,
			})
			devID++
		}
	}
	return devices
}

// initializeDeviceLookupMaps builds the indexes used by PrepareResourceClaims
// before kubelet can call into the plugin. ResourceSlice publication must not
// be required to populate these maps.
func (cp *CPUDriver) initializeDeviceLookupMaps() {
	cp.deviceNameToCPUID = make(map[string]int)
	cp.deviceNameToSocketID = make(map[string]int)
	cp.deviceNameToNUMANodeID = make(map[string]int)

	if cp.cpuDeviceMode == CPU_DEVICE_MODE_GROUPED {
		for _, device := range cp.groupedCPUDeviceInfos() {
			switch cp.cpuDeviceGroupBy {
			case GROUP_BY_SOCKET:
				cp.deviceNameToSocketID[device.name] = device.socketID
			case GROUP_BY_NUMA_NODE:
				cp.deviceNameToNUMANodeID[device.name] = device.numaNodeID
			}
		}
		return
	}

	for _, device := range cp.cpuDeviceInfos() {
		cp.deviceNameToCPUID[device.name] = device.cpu.CpuID
	}
}

// createGroupedCPUDeviceSlices creates Device objects based on the CPU topology, grouped by a specific criteria.
func (cp *CPUDriver) createGroupedCPUDeviceSlices(logger logr.Logger) [][]resourceapi.Device {
	logger.V(4).Info("creating grouped CPU devices")
	var devices []resourceapi.Device

	for _, deviceInfo := range cp.groupedCPUDeviceInfos() {
		availableCPUs := int64(deviceInfo.cpus.Size())
		deviceCapacity := map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
			cpuResourceQualifiedName: {Value: *resource.NewQuantity(availableCPUs, resource.DecimalSI)},
		}

		switch cp.cpuDeviceGroupBy {
		case GROUP_BY_SOCKET:
			deviceAttrs := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				AttributeSocketID:   {IntValue: ptr.To(int64(deviceInfo.socketID))},
				AttributeNumCPUs:    {IntValue: ptr.To(availableCPUs)},
				AttributeSMTEnabled: {BoolValue: ptr.To(cp.cpuTopology.SMTEnabled)},
			}
			cp.setPCIeRootsAttribute(deviceAttrs, deviceInfo.cpus.UnsortedList()...)

			devices = append(devices, resourceapi.Device{
				Name:                     deviceInfo.name,
				Attributes:               deviceAttrs,
				Capacity:                 deviceCapacity,
				AllowMultipleAllocations: ptr.To(true),
			})
		case GROUP_BY_NUMA_NODE:
			deviceAttrs := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				AttributeNUMANodeID: {IntValue: ptr.To(int64(deviceInfo.numaNodeID))},
				AttributeSocketID:   {IntValue: ptr.To(int64(deviceInfo.socketID))},
				AttributeSMTEnabled: {BoolValue: ptr.To(cp.cpuTopology.SMTEnabled)},
				AttributeNumCPUs:    {IntValue: ptr.To(availableCPUs)},
			}
			device.SetCompatibilityAttributes(deviceAttrs, int64(deviceInfo.numaNodeID))
			cp.setPCIeRootsAttribute(deviceAttrs, deviceInfo.cpus.UnsortedList()...)

			devices = append(devices, resourceapi.Device{
				Name:                     deviceInfo.name,
				Attributes:               deviceAttrs,
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
	var allDevices []resourceapi.Device
	for _, deviceInfo := range cp.cpuDeviceInfos() {
		cpu := deviceInfo.cpu
		deviceAttrs := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
			AttributeNUMANodeID: {IntValue: ptr.To(int64(cpu.NUMANodeID))},
			AttributeSocketID:   {IntValue: ptr.To(int64(cpu.SocketID))},
			AttributeSMTEnabled: {BoolValue: ptr.To(cp.cpuTopology.SMTEnabled)},
			AttributeCacheL3ID:  {IntValue: ptr.To(int64(cpu.UncoreCacheID))},
			AttributeCoreType:   {StringValue: ptr.To(cpu.CoreType.String())},
			AttributeCoreID:     {IntValue: ptr.To(int64(cpu.CoreID))},
			AttributeCPUID:      {IntValue: ptr.To(int64(cpu.CpuID))},
		}
		device.SetCompatibilityAttributes(deviceAttrs, int64(cpu.NUMANodeID))
		cp.setPCIeRootsAttribute(deviceAttrs, cpu.CpuID)

		cpuDevice := resourceapi.Device{
			Name:       deviceInfo.name,
			Attributes: deviceAttrs,
			Capacity:   make(map[resourceapi.QualifiedName]resourceapi.DeviceCapacity),
		}
		allDevices = append(allDevices, cpuDevice)
	}

	if len(allDevices) == 0 {
		return nil
	}

	// Chunk devices into slices of at most devicesPerResourceSlice
	return slices.Collect(slices.Chunk(allDevices, cp.devicesPerResourceSlice))
}

// PublishResources publishes ResourceSlice for CPU resources.
func (cp *CPUDriver) PublishResources(ctx context.Context) {
	ctx, logger := ctxlog.WithValues(ctx, "opID", generateShortID(opIDLen), "deviceMode", cp.cpuDeviceMode, "groupBy", cp.cpuDeviceGroupBy)

	logger.V(4).Info("begin: publishing resources")
	defer logger.V(4).Info("end: publishing resources")

	var deviceChunks [][]resourceapi.Device
	if cp.cpuDeviceMode == CPU_DEVICE_MODE_GROUPED {
		deviceChunks = cp.createGroupedCPUDeviceSlices(logger)
	} else {
		deviceChunks = cp.createCPUDeviceSlices()
	}

	if deviceChunks == nil {
		logger.Info("no devices to publish or error occurred")
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
		logger.Error(err, "error publishing resources")
	}
}

// PrepareResourceClaims is called by the kubelet to prepare a resource claim.
func (cp *CPUDriver) PrepareResourceClaims(ctx context.Context, claims []*resourceapi.ResourceClaim) (map[types.UID]kubeletplugin.PrepareResult, error) {
	_, logger := ctxlog.WithValues(ctx, "opID", generateShortID(opIDLen))

	logger.V(4).Info("begin: preparing resource claims", "numClaims", len(claims))
	defer logger.V(4).Info("end: preparing resource claims", "numClaims", len(claims))

	result := make(map[types.UID]kubeletplugin.PrepareResult)

	if len(claims) == 0 {
		return result, nil
	}

	for _, claim := range claims {
		cLogger := logger.WithValues("claim", ctxlog.KObj(claim), "claimUID", claim.UID)
		if cp.cpuDeviceMode == CPU_DEVICE_MODE_GROUPED {
			result[claim.UID] = cp.prepareGroupedResourceClaim(cLogger, claim)
		} else {
			result[claim.UID] = cp.prepareResourceClaim(cLogger, claim)
		}
	}
	return result, nil
}

func getCDIDeviceName(uid types.UID) string {
	return fmt.Sprintf("claim-%s", uid)
}

func (cp *CPUDriver) prepareGroupedResourceClaim(logger logr.Logger, claim *resourceapi.ResourceClaim) kubeletplugin.PrepareResult {
	logger.V(4).Info("preparing grouped resource claim")

	if claim.Status.Allocation == nil {
		return kubeletplugin.PrepareResult{
			Err: fmt.Errorf("claim %s/%s has no allocation", claim.Namespace, claim.Name),
		}
	}

	var cpuAssignment cpuset.CPUSet
	sharedCPUs := cp.cpuAllocationStore.GetSharedCPUs()
	for _, alloc := range claim.Status.Allocation.Devices.Results {
		claimCPUCount := int64(0)
		if alloc.Driver != cp.driverName {
			continue
		}
		if quantity, ok := alloc.ConsumedCapacity[cpuResourceQualifiedName]; ok {
			count := quantity.Value()
			claimCPUCount = count
			logger.V(4).Info("found CPU request", "numCPUs", count, "device", alloc.Device)
		}

		topo := cp.cpuTopology

		var availableCPUsForDevice cpuset.CPUSet
		if cp.cpuDeviceGroupBy == GROUP_BY_SOCKET {
			socketID, ok := cp.deviceNameToSocketID[alloc.Device]
			if !ok {
				return kubeletplugin.PrepareResult{Err: fmt.Errorf("no valid socket ID found for device %s", alloc.Device)}
			}
			socketCPUs := topo.CPUDetails.CPUsInSockets(socketID)
			availableCPUsForDevice = sharedCPUs.Difference(cpuAssignment).Intersection(socketCPUs)
			logger.V(4).Info("socket CPU availability", "socketID", socketID, "socketCPUs", socketCPUs.String(), "availableCPUs", availableCPUsForDevice.String())
		} else { // numanode
			numaNodeID, ok := cp.deviceNameToNUMANodeID[alloc.Device]
			if !ok {
				return kubeletplugin.PrepareResult{Err: fmt.Errorf("no valid NUMA node ID found for device %s", alloc.Device)}
			}
			numaCPUs := topo.CPUDetails.CPUsInNUMANodes(numaNodeID)
			availableCPUsForDevice = sharedCPUs.Difference(cpuAssignment).Intersection(numaCPUs)
			logger.V(4).Info("NUMA node CPU availability", "numaNodeID", numaNodeID, "numaCPUs", numaCPUs.String(), "availableCPUs", availableCPUsForDevice.String())
		}

		cur, err := cpumanager.TakeByTopologyNUMAPacked(logger, topo, availableCPUsForDevice, int(claimCPUCount), cpumanager.CPUSortingStrategyPacked, true)
		if err != nil {
			return kubeletplugin.PrepareResult{Err: err}
		}
		cpuAssignment = cpuAssignment.Union(cur)
		logger.V(2).Info("CPU assignment for device", "device", alloc.Device, "assigned", cur.String(), "allAssigned", cpuAssignment.String())
	}

	if cpuAssignment.Size() == 0 {
		logger.V(6).Info("claim has no CPU allocations for this driver")
		return kubeletplugin.PrepareResult{}
	}

	cp.cpuAllocationStore.AddResourceClaimAllocation(logger, claim.UID, cpuAssignment)

	deviceName := getCDIDeviceName(claim.UID)
	envVar := fmt.Sprintf("%s_%s=%s", cdiEnvVarPrefix, claim.UID, cpuAssignment.String())
	if err := cp.cdiMgr.AddDevice(logger, deviceName, envVar); err != nil {
		return kubeletplugin.PrepareResult{Err: err}
	}

	qualifiedName := cdiparser.QualifiedName(cdiVendor, cdiClass, deviceName)
	logger.V(6).Info("prepared CDI device", "cdiDeviceName", deviceName, "envVar", envVar, "qualifiedName", qualifiedName)
	preparedDevices := []kubeletplugin.Device{}
	for _, allocResult := range claim.Status.Allocation.Devices.Results {
		if allocResult.Driver != cp.driverName {
			continue
		}
		preparedDevice := kubeletplugin.Device{
			PoolName:     allocResult.Pool,
			DeviceName:   allocResult.Device,
			CDIDeviceIDs: []string{qualifiedName},
			Requests:     []string{allocResult.Request},
		}
		preparedDevices = append(preparedDevices, preparedDevice)
	}

	logger.V(4).Info("prepared devices for grouped resource claim", "preparedDevices", preparedDevices)
	return kubeletplugin.PrepareResult{
		Devices: preparedDevices,
	}
}

func (cp *CPUDriver) prepareResourceClaim(logger logr.Logger, claim *resourceapi.ResourceClaim) kubeletplugin.PrepareResult {
	logger.V(4).Info("preparing individual resource claim")

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
		logger.V(6).Info("claim has no CPU allocations for this driver")
		return kubeletplugin.PrepareResult{}
	}

	claimCPUSet := cpuset.New(claimCPUIDs...)
	// All the CPUs allocated to a claim should currently be in the shared pool.
	sharedCPUs := cp.cpuAllocationStore.GetSharedCPUs()
	if !claimCPUSet.IsSubsetOf(sharedCPUs) {
		return kubeletplugin.PrepareResult{
			Err: fmt.Errorf("claim %s/%s has overlapping device assignment with other claims", claim.Namespace, claim.Name),
		}
	}

	cp.cpuAllocationStore.AddResourceClaimAllocation(logger, claim.UID, claimCPUSet)
	deviceName := getCDIDeviceName(claim.UID)
	envVar := fmt.Sprintf("%s_%s=%s", cdiEnvVarPrefix, claim.UID, claimCPUSet.String())
	if err := cp.cdiMgr.AddDevice(logger, deviceName, envVar); err != nil {
		return kubeletplugin.PrepareResult{Err: err}
	}

	qualifiedName := cdiparser.QualifiedName(cdiVendor, cdiClass, deviceName)
	logger.V(6).Info("prepared CDI device", "cdiDeviceName", deviceName, "envVar", envVar, "qualifiedName", qualifiedName)
	preparedDevices := []kubeletplugin.Device{}
	for _, allocResult := range claim.Status.Allocation.Devices.Results {
		if allocResult.Driver != cp.driverName {
			continue
		}
		preparedDevice := kubeletplugin.Device{
			PoolName:     allocResult.Pool,
			DeviceName:   allocResult.Device,
			CDIDeviceIDs: []string{qualifiedName},
		}
		preparedDevices = append(preparedDevices, preparedDevice)
	}

	logger.V(4).Info("prepared devices for individual resource claim", "preparedDevices", preparedDevices)
	return kubeletplugin.PrepareResult{
		Devices: preparedDevices,
	}
}

// UnprepareResourceClaims is called by the kubelet to unprepare the resources for a claim.
func (cp *CPUDriver) UnprepareResourceClaims(ctx context.Context, claims []kubeletplugin.NamespacedObject) (map[types.UID]error, error) {
	_, logger := ctxlog.WithValues(ctx, "opID", generateShortID(opIDLen))

	logger.V(4).Info("begin: unpreparing resource claims", "numClaims", len(claims))
	defer logger.V(4).Info("end: unpreparing resource claims", "numClaims", len(claims))

	result := make(map[types.UID]error)

	if len(claims) == 0 {
		return result, nil
	}

	for _, claim := range claims {
		// note kubeletplugin.NamespacedObject doesn't implement KMetadata
		cLogger := logger.WithValues("claim", claim.String(), "claimUID", claim.UID)
		cLogger.V(2).Info("unpreparing resource claim")
		err := cp.unprepareResourceClaim(cLogger, claim)
		result[claim.UID] = err
		if err != nil {
			cLogger.Error(err, "error unpreparing resources for claim")
		}
	}
	return result, nil
}

func (cp *CPUDriver) unprepareResourceClaim(logger logr.Logger, claim kubeletplugin.NamespacedObject) error {
	// Remove the CDI spec first. If that fails, keep the allocation recorded so
	// the driver does not make those CPUs available while stale CDI state remains.
	if err := cp.cdiMgr.RemoveDevice(logger, getCDIDeviceName(claim.UID)); err != nil {
		return err
	}
	cp.cpuAllocationStore.RemoveResourceClaimAllocation(logger, claim.UID)
	return nil
}

// HandleError is called by the kubelet plugin framework when an error occurs in the background,
// for example while publishing ResourceSlices.
func (cp *CPUDriver) HandleError(ctx context.Context, err error, msg string) {
	logger := ctxlog.FromContext(ctx)

	// Log the error using the standard Kubernetes error handler
	runtime.HandleErrorWithContext(ctx, err, msg)

	// For unrecoverable errors, exit immediately with a clear error message.
	// This fail-fast behavior is intentional for early project maturity to surface
	// issues quickly rather than silently continuing in a broken state.
	if !errors.Is(err, kubeletplugin.ErrRecoverable) {
		logger.Error(err, "fatal unrecoverable error in DRA driver, exiting",
			"driver", cp.driverName,
			"node", cp.nodeName,
			"message", msg,
		)
		ctxlog.Flush()
		os.Exit(1)
	}
}

func (cp *CPUDriver) setPCIeRootsAttribute(attrs map[resourceapi.QualifiedName]resourceapi.DeviceAttribute, cpuIDs ...int) {
	// Note: union semantics are correct because kernel cpulistaffinity currently collapses to NUMA granularity;
	// grouped allocation at socket/NUMA level therefore covers all CPUs local to every reported root.
	// See docs/dev/topology-linux-sysfs.md for in-depth exploration about the topic.
	pcieRoots := cp.pcieRootMapper.GetPCIeRootsForCPU(cpuIDs...)
	if len(pcieRoots) == 0 {
		return
	}
	attrs[deviceattribute.StandardDeviceAttributePCIeRoot] = resourceapi.DeviceAttribute{StringValues: pcieRoots}
}
