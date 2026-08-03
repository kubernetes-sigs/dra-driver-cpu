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

package device

import (
	"fmt"
	"sort"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/utils/cpuset"
)

const (
	CPUResourceQualifiedName = "dra.cpu/cpu"

	CPUDevicePrefix              = "cpudev"
	CPUDeviceSocketGroupedPrefix = "cpudevsocket"
	CPUDeviceNUMAGroupedPrefix   = "cpudevnuma"
	CPUDeviceMachineGrouped      = "cpudevmachine"
)

func Build(topo *cpuinfo.CPUTopology, reservedCPUSet cpuset.CPUSet, pcieRootMapper *store.PCIeRootMapper) ([]resourceapi.Device, map[string]int) {
	deviceInfos := cpuDeviceInfos(topo, reservedCPUSet)
	nameToID := make(map[string]int)
	for _, dev := range deviceInfos {
		nameToID[dev.name] = dev.cpu.CpuID
	}
	return createCPUDeviceSlices(deviceInfos, pcieRootMapper, topo.SMTEnabled), nameToID
}

func BuildGrouped(logger logr.Logger, groupBy string, topo *cpuinfo.CPUTopology, onlineCPUs, reservedCPUSet cpuset.CPUSet, pcieRootMapper *store.PCIeRootMapper, exposeCPUSet bool) ([]resourceapi.Device, map[string]int) {
	deviceInfos := groupedCPUDeviceInfos(groupBy, topo, onlineCPUs, reservedCPUSet)
	nameToID := make(map[string]int)
	for _, dev := range deviceInfos {
		switch groupBy {
		case GROUP_BY_SOCKET:
			nameToID[dev.name] = dev.socketID
		case GROUP_BY_NUMA_NODE:
			nameToID[dev.name] = dev.numaNodeID
		}
	}
	return createGroupedCPUDeviceSlices(logger, groupBy, deviceInfos, pcieRootMapper, topo.SMTEnabled, exposeCPUSet), nameToID
}

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

func groupedCPUDeviceInfos(groupBy string, topo *cpuinfo.CPUTopology, onlineCPUs, reservedCPUs cpuset.CPUSet) []groupedCPUDeviceInfo {
	var devices []groupedCPUDeviceInfo

	switch groupBy {
	case GROUP_BY_SOCKET:
		socketIDs := topo.CPUDetails.Sockets().List()
		for _, socketID := range socketIDs {
			allocatableCPUs := topo.CPUDetails.CPUsInSockets(socketID).Difference(reservedCPUs)
			if allocatableCPUs.Size() == 0 {
				continue
			}
			devices = append(devices, groupedCPUDeviceInfo{
				name:     fmt.Sprintf("%s%03d", CPUDeviceSocketGroupedPrefix, socketID),
				cpus:     allocatableCPUs,
				socketID: socketID,
			})
		}
	case GROUP_BY_NUMA_NODE:
		numaNodeIDs := topo.CPUDetails.NUMANodes().List()
		for _, numaID := range numaNodeIDs {
			allocatableCPUs := topo.CPUDetails.CPUsInNUMANodes(numaID).Difference(reservedCPUs)
			if allocatableCPUs.Size() == 0 {
				continue
			}

			// All CPUs in a NUMA node belong to the same socket.
			anyCPU := allocatableCPUs.UnsortedList()[0]
			devices = append(devices, groupedCPUDeviceInfo{
				name:       fmt.Sprintf("%s%03d", CPUDeviceNUMAGroupedPrefix, numaID),
				cpus:       allocatableCPUs,
				socketID:   topo.CPUDetails[anyCPU].SocketID,
				numaNodeID: numaID,
			})
		}
	case GROUP_BY_MACHINE:
		allocatableCPUs := onlineCPUs.Difference(reservedCPUs)
		devices = append(devices, groupedCPUDeviceInfo{
			name: CPUDeviceMachineGrouped,
			cpus: allocatableCPUs,
		})
	}
	return devices
}

// cpuDeviceInfos returns the stable individual CPU device enumeration used by
// both ResourceSlice publication and PrepareResourceClaims device lookup.
// Keep the ordering in one place so device names resolve to the same CPUs even
// when Prepare runs before the first ResourceSlice publication after restart.
func cpuDeviceInfos(topo *cpuinfo.CPUTopology, reservedCPUSet cpuset.CPUSet) []cpuDeviceInfo {
	reservedCPUs := make(map[int]bool)
	for _, cpuID := range reservedCPUSet.List() {
		reservedCPUs[cpuID] = true
	}

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
				name: fmt.Sprintf("%s%03d", CPUDevicePrefix, devID),
				cpu:  cpu,
			})
			devID++
		}
	}
	return devices
}

// createGroupedCPUDeviceSlices creates Device objects based on the CPU topology, grouped by a specific criteria.
func createGroupedCPUDeviceSlices(logger logr.Logger, groupBy string, deviceInfos []groupedCPUDeviceInfo, pcieRootMapper *store.PCIeRootMapper, smtEnabled, exposeCPUSet bool) []resourceapi.Device {
	logger.V(4).Info("creating grouped CPU devices")
	var devices []resourceapi.Device

	for _, deviceInfo := range deviceInfos {
		availableCPUs := int64(deviceInfo.cpus.Size())
		deviceCapacity := map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
			CPUResourceQualifiedName: {Value: *resource.NewQuantity(availableCPUs, resource.DecimalSI)},
		}

		switch groupBy {
		case GROUP_BY_SOCKET:
			deviceAttrs := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				AttributeSocketID:   {IntValue: new(int64(deviceInfo.socketID))},
				AttributeNumCPUs:    {IntValue: new(availableCPUs)},
				AttributeSMTEnabled: {BoolValue: new(smtEnabled)},
			}
			addPCIeRootsAttribute(pcieRootMapper, deviceAttrs, deviceInfo.cpus.UnsortedList()...)
			if exposeCPUSet {
				deviceAttrs[AttributeCPUIDs] = resourceapi.DeviceAttribute{
					// CRITICAL NOTE: the attribute limit is still *not* solved,
					// just less likely
					StringValue: new(deviceInfo.cpus.String()),
				}
			}

			devices = append(devices, resourceapi.Device{
				Name:                     deviceInfo.name,
				Attributes:               deviceAttrs,
				Capacity:                 deviceCapacity,
				AllowMultipleAllocations: new(true),
			})
		case GROUP_BY_NUMA_NODE:
			deviceAttrs := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				AttributeNUMANodeID: {IntValue: new(int64(deviceInfo.numaNodeID))},
				AttributeSocketID:   {IntValue: new(int64(deviceInfo.socketID))},
				AttributeSMTEnabled: {BoolValue: new(smtEnabled)},
				AttributeNumCPUs:    {IntValue: new(availableCPUs)},
			}
			if exposeCPUSet {
				deviceAttrs[AttributeCPUIDs] = resourceapi.DeviceAttribute{
					// CRITICAL NOTE: the attribute limit is still *not* solved,
					// just less likely
					StringValue: new(deviceInfo.cpus.String()),
				}
			}
			SetCompatibilityAttributes(deviceAttrs, int64(deviceInfo.numaNodeID))
			addPCIeRootsAttribute(pcieRootMapper, deviceAttrs, deviceInfo.cpus.UnsortedList()...)

			devices = append(devices, resourceapi.Device{
				Name:                     deviceInfo.name,
				Attributes:               deviceAttrs,
				Capacity:                 deviceCapacity,
				AllowMultipleAllocations: new(true),
			})
		case GROUP_BY_MACHINE:
			deviceAttrs := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				AttributeSMTEnabled: {BoolValue: new(smtEnabled)},
				AttributeNumCPUs:    {IntValue: new(availableCPUs)},
			}
			addPCIeRootsAttribute(pcieRootMapper, deviceAttrs, deviceInfo.cpus.UnsortedList()...)
			devices = append(devices, resourceapi.Device{
				Name:                     deviceInfo.name,
				Attributes:               deviceAttrs,
				Capacity:                 deviceCapacity,
				AllowMultipleAllocations: new(true),
			})
		}
	}

	return devices
}

// createCPUDeviceSlices creates Device objects based on the CPU topology.
// It groups CPUs by physical core to assign consecutive device IDs to hyperthreads.
// This allows the DRA scheduler, which requests resources in contiguous blocks,
// to co-locate workloads on hyperthreads of the same core.
func createCPUDeviceSlices(deviceInfos []cpuDeviceInfo, pcieRootMapper *store.PCIeRootMapper, smtEnabled bool) []resourceapi.Device {
	var allDevices []resourceapi.Device
	for _, deviceInfo := range deviceInfos {
		cpu := deviceInfo.cpu
		deviceAttrs := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
			AttributeNUMANodeID: {IntValue: new(int64(cpu.NUMANodeID))},
			AttributeSocketID:   {IntValue: new(int64(cpu.SocketID))},
			AttributeSMTEnabled: {BoolValue: new(smtEnabled)},
			AttributeCacheL3ID:  {IntValue: new(int64(cpu.UncoreCacheID))},
			AttributeCoreType:   {StringValue: new(cpu.CoreType.String())},
			AttributeCoreID:     {IntValue: new(int64(cpu.CoreID))},
			AttributeCPUID:      {IntValue: new(int64(cpu.CpuID))},
		}
		SetCompatibilityAttributes(deviceAttrs, int64(cpu.NUMANodeID))
		addPCIeRootsAttribute(pcieRootMapper, deviceAttrs, cpu.CpuID)

		cpuDevice := resourceapi.Device{
			Name:       deviceInfo.name,
			Attributes: deviceAttrs,
			Capacity:   make(map[resourceapi.QualifiedName]resourceapi.DeviceCapacity),
		}
		allDevices = append(allDevices, cpuDevice)
	}
	return allDevices
}

func addPCIeRootsAttribute(pcieRootMapper *store.PCIeRootMapper, attrs map[resourceapi.QualifiedName]resourceapi.DeviceAttribute, cpuIDs ...int) {
	// Note: union semantics are correct because kernel cpulistaffinity currently collapses to NUMA granularity;
	// grouped allocation at socket/NUMA level therefore covers all CPUs local to every reported root.
	// See docs/dev/topology-linux-sysfs.md for in-depth exploration about the topic.
	pcieRoots := pcieRootMapper.GetPCIeRootsForCPU(cpuIDs...)
	if len(pcieRoots) == 0 {
		return
	}
	attrs[deviceattribute.StandardDeviceAttributePCIeRoot] = resourceapi.DeviceAttribute{StringValues: pcieRoots}
}
