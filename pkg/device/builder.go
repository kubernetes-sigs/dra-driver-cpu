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

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	v1 "k8s.io/api/core/v1"
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

// Inventory holds the machine data and resource availability
// as configured. Represents a subset of the physical resources.
type Inventory struct {
	CPUTopology  *cpuinfo.CPUTopology
	ReservedCPUs cpuset.CPUSet
}

// ManagedCPUs returns CPUs reports the validated online CPUs.
func (inv Inventory) ManagedCPUs() cpuset.CPUSet {
	return inv.CPUTopology.CPUDetails.CPUs()
}

// AllocatableCPUs returns all the machine-level CPUs available for allocation
// NOTE this DOES NOT take into account currently allocated CPUs: it returns
// the maximum available theoretical pool depending only on static configuration
func (inv Inventory) AllocatableCPUs() cpuset.CPUSet {
	return inv.ManagedCPUs().Difference(inv.ReservedCPUs)
}

// Mapping includes the reverse lookup maps to bind the exposed DRA device names
// to the pool we need to allocate resources from.
// At most one lookup map is populated. LayoutMachine does not populate one, by design.
type Mapping struct {
	// NameToCPUID is populated for LayoutIndividual.
	NameToCPUID map[string]int
	// NameToSocketID is populated for LayoutSocket.
	NameToSocketID map[string]int
	// NameToNUMANodeID is populated for LayoutNUMA.
	NameToNUMANodeID map[string]int
}

// Layout controls how CPUs are exposed as DRA devices.
// It abstracts the driver configuration grouping mode and criteria on a single
// device layer selector.
type Layout string

const (
	// LayoutIndividual exposes each CPU as a separate device.
	LayoutIndividual Layout = "individual"
	// LayoutSocket exposes one device per CPU socket.
	LayoutSocket Layout = "socket"
	// LayoutNUMA exposes one device per NUMA node.
	LayoutNUMA Layout = "numa"
	// LayoutMachine exposes one device for all allocatable CPUs in the machine.
	LayoutMachine Layout = "machine"
)

// FindLayout converts the driver configuration into a device layout.
func FindLayout(cpuDeviceMode, cpuDeviceGroupBy string) Layout {
	if cpuDeviceMode != CPU_DEVICE_MODE_GROUPED {
		return LayoutIndividual
	}
	switch cpuDeviceGroupBy {
	case GROUP_BY_SOCKET:
		return LayoutSocket
	case GROUP_BY_NUMA_NODE:
		return LayoutNUMA
	default:
		return LayoutMachine
	}
}

// BuildInput is the input for Build. It carries both input parameters and input data.
type BuildInput struct {
	// Inventory describes the CPU topology and CPU availability to expose.
	Inventory Inventory
	// Layout controls how CPUs are exposed as devices.
	Layout Layout
	// PCIeRootMapper provides the PCIe roots associated with CPUs.
	PCIeRootMapper *store.PCIeRootMapper
	// PublishNodeAllocatableResourceMapping enables node allocatable resource
	// mappings on the generated devices.
	PublishNodeAllocatableResourceMapping bool
	// Expose attributes intended for consumption of an external allocator:
	// - cpuset pertaining to a grouped device as attribute
	// - smt sibling maapping
	ExposeExtAttrs bool
}

type BuildResult struct {
	// Devices are the generated DRA devices.
	Devices []resourceapi.Device
	Mapping Mapping
}

func Build(input BuildInput) (BuildResult, error) {
	if input.Layout == LayoutIndividual {
		nameToID := make(map[string]int)
		deviceInfos := cpuDeviceInfos(input.Inventory)
		for _, dev := range deviceInfos {
			nameToID[dev.name] = dev.cpu.CpuID
		}
		devices, err := createCPUDeviceSlices(deviceInfos, input.PCIeRootMapper, input.Inventory.CPUTopology, input.PublishNodeAllocatableResourceMapping)
		if err != nil {
			return BuildResult{}, err
		}
		return BuildResult{
			Devices: devices,
			Mapping: Mapping{
				NameToCPUID: nameToID,
			},
		}, nil
	}

	var err error
	var res BuildResult
	switch input.Layout {
	case LayoutSocket:
		res.Mapping.NameToSocketID = make(map[string]int)
	case LayoutNUMA:
		res.Mapping.NameToNUMANodeID = make(map[string]int)
	}
	deviceInfos := groupedCPUDeviceInfos(input.Layout, input.Inventory)
	for _, dev := range deviceInfos {
		switch input.Layout {
		case LayoutSocket:
			res.Mapping.NameToSocketID[dev.name] = dev.socketID
		case LayoutNUMA:
			res.Mapping.NameToNUMANodeID[dev.name] = dev.numaNodeID
		}
	}
	res.Devices, err = createGroupedCPUDeviceSlices(input.Layout, deviceInfos, input.PCIeRootMapper, input.Inventory.CPUTopology, input.PublishNodeAllocatableResourceMapping, input.ExposeExtAttrs)
	if err != nil {
		return BuildResult{}, err
	}
	return res, nil
}

func groupedCPUNodeAllocatable(enabled bool) map[v1.ResourceName]resourceapi.NodeAllocatableResource {
	if !enabled {
		return nil
	}
	return map[v1.ResourceName]resourceapi.NodeAllocatableResource{
		v1.ResourceCPU: {
			Mapping: &resourceapi.NodeAllocatableMapping{
				CapacityKey:        new(resourceapi.QualifiedName(CPUResourceQualifiedName)),
				CapacityMultiplier: new(resource.MustParse("1")),
			},
		},
	}
}

func individualCPUNodeAllocatable(enabled bool) map[v1.ResourceName]resourceapi.NodeAllocatableResource {
	if !enabled {
		return nil
	}
	return map[v1.ResourceName]resourceapi.NodeAllocatableResource{
		v1.ResourceCPU: {
			Mapping: &resourceapi.NodeAllocatableMapping{
				DeviceMultiplier: new(resource.MustParse("1")),
			},
		},
	}
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

func groupedCPUDeviceInfos(layout Layout, machine Inventory) []groupedCPUDeviceInfo {
	var devices []groupedCPUDeviceInfo

	topo := machine.CPUTopology // shortcut

	switch layout {
	case LayoutSocket:
		socketIDs := topo.CPUDetails.Sockets().List()
		for _, socketID := range socketIDs {
			allocatableCPUs := topo.CPUDetails.CPUsInSockets(socketID).Difference(machine.ReservedCPUs)
			if allocatableCPUs.Size() == 0 {
				continue
			}
			devices = append(devices, groupedCPUDeviceInfo{
				name:     fmt.Sprintf("%s%03d", CPUDeviceSocketGroupedPrefix, socketID),
				cpus:     allocatableCPUs,
				socketID: socketID,
			})
		}
	case LayoutNUMA:
		numaNodeIDs := topo.CPUDetails.NUMANodes().List()
		for _, numaID := range numaNodeIDs {
			allocatableCPUs := topo.CPUDetails.CPUsInNUMANodes(numaID).Difference(machine.ReservedCPUs)
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
	case LayoutMachine:
		// Use the topology-validated CPU set. GetCPUInfos filters online CPUs
		// whose topology is incomplete, and the allocation store is built from
		// the same CPUDetails map.
		devices = append(devices, groupedCPUDeviceInfo{
			name: CPUDeviceMachineGrouped,
			cpus: machine.AllocatableCPUs(),
		})
	}
	return devices
}

// cpuDeviceInfos returns the stable individual CPU device enumeration used by
// both ResourceSlice publication and PrepareResourceClaims device lookup.
// Keep the ordering in one place so device names resolve to the same CPUs even
// when Prepare runs before the first ResourceSlice publication after restart.
func cpuDeviceInfos(machine Inventory) []cpuDeviceInfo {
	topo := machine.CPUTopology // shortcut

	reservedCPUs := make(map[int]bool)
	for _, cpuID := range machine.ReservedCPUs.List() {
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

func createGroupedCPUDeviceSlices(layout Layout, deviceInfos []groupedCPUDeviceInfo, pcieRootMapper *store.PCIeRootMapper, topo *cpuinfo.CPUTopology, nodeAllocatableResources, exposeExtAttrs bool) ([]resourceapi.Device, error) {
	var devices []resourceapi.Device

	for _, deviceInfo := range deviceInfos {
		availableCPUs := int64(deviceInfo.cpus.Size())
		deviceCapacity := map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{
			CPUResourceQualifiedName: {Value: *resource.NewQuantity(availableCPUs, resource.DecimalSI)},
		}

		switch layout {
		case LayoutSocket:
			deviceAttrs := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				AttributeSocketID: {IntValue: new(int64(deviceInfo.socketID))},
				AttributeNumCPUs:  {IntValue: new(availableCPUs)},
				AttributeSMTLevel: {IntValue: new(int64(topo.SMTLevel))},
			}
			addCompatibilityAttributes(deviceAttrs, -1, topo.SMTEnabled)
			if err := addPCIeRootsAttribute(pcieRootMapper, deviceAttrs, deviceInfo.cpus.UnsortedList()...); err != nil {
				return nil, err
			}
			if exposeExtAttrs {
				if err := addCPUIDsAttribute(deviceAttrs, deviceInfo.cpus); err != nil {
					return nil, err
				}
				if err := addSMTMapAttribute(deviceAttrs, topo); err != nil {
					return nil, err
				}
			}

			devices = append(devices, resourceapi.Device{
				Name:                     deviceInfo.name,
				Attributes:               deviceAttrs,
				Capacity:                 deviceCapacity,
				AllowMultipleAllocations: new(true),
				NodeAllocatableResources: groupedCPUNodeAllocatable(nodeAllocatableResources),
			})
		case LayoutNUMA:
			deviceAttrs := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				// DRA standard attributes first
				deviceattribute.StandardDeviceAttributeNUMANode: {IntValue: new(int64(deviceInfo.numaNodeID))},
				// Driver-specific/non-standard attributes next
				AttributeSocketID: {IntValue: new(int64(deviceInfo.socketID))},
				AttributeNumCPUs:  {IntValue: new(availableCPUs)},
				AttributeSMTLevel: {IntValue: new(int64(topo.SMTLevel))},
			}
			addCompatibilityAttributes(deviceAttrs, int64(deviceInfo.numaNodeID), topo.SMTEnabled)
			if err := addPCIeRootsAttribute(pcieRootMapper, deviceAttrs, deviceInfo.cpus.UnsortedList()...); err != nil {
				return nil, err
			}
			if exposeExtAttrs {
				if err := addCPUIDsAttribute(deviceAttrs, deviceInfo.cpus); err != nil {
					return nil, err
				}
				if err := addSMTMapAttribute(deviceAttrs, topo); err != nil {
					return nil, err
				}
			}

			devices = append(devices, resourceapi.Device{
				Name:                     deviceInfo.name,
				Attributes:               deviceAttrs,
				Capacity:                 deviceCapacity,
				AllowMultipleAllocations: new(true),
				NodeAllocatableResources: groupedCPUNodeAllocatable(nodeAllocatableResources),
			})
		case LayoutMachine:
			deviceAttrs := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				AttributeNumCPUs:  {IntValue: new(availableCPUs)},
				AttributeSMTLevel: {IntValue: new(int64(topo.SMTLevel))},
			}
			addCompatibilityAttributes(deviceAttrs, -1, topo.SMTEnabled)
			if err := addPCIeRootsAttribute(pcieRootMapper, deviceAttrs, deviceInfo.cpus.UnsortedList()...); err != nil {
				return nil, err
			}
			if exposeExtAttrs {
				if err := addCPUIDsAttribute(deviceAttrs, deviceInfo.cpus); err != nil {
					return nil, err
				}
				if err := addSMTMapAttribute(deviceAttrs, topo); err != nil {
					return nil, err
				}
			}
			devices = append(devices, resourceapi.Device{
				Name:                     deviceInfo.name,
				Attributes:               deviceAttrs,
				Capacity:                 deviceCapacity,
				AllowMultipleAllocations: new(true),
				NodeAllocatableResources: groupedCPUNodeAllocatable(nodeAllocatableResources),
			})
		}
	}

	return devices, nil
}

// createCPUDeviceSlices creates Device objects based on the CPU topology.
// It groups CPUs by physical core to assign consecutive device IDs to hyperthreads.
// This allows the DRA scheduler, which requests resources in contiguous blocks,
// to co-locate workloads on hyperthreads of the same core.
func createCPUDeviceSlices(deviceInfos []cpuDeviceInfo, pcieRootMapper *store.PCIeRootMapper, topo *cpuinfo.CPUTopology, nodeAllocatableResources bool) ([]resourceapi.Device, error) {
	var allDevices []resourceapi.Device
	for _, deviceInfo := range deviceInfos {
		cpu := deviceInfo.cpu
		deviceAttrs := map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
			// DRA standard attributes first
			deviceattribute.StandardDeviceAttributeNUMANode: {IntValue: new(int64(cpu.NUMANodeID))},
			// Driver-specific/non-standard attributes next
			AttributeSocketID:  {IntValue: new(int64(cpu.SocketID))},
			AttributeCacheL3ID: {IntValue: new(int64(cpu.UncoreCacheID))},
			AttributeCoreType:  {StringValue: new(cpu.CoreType.String())},
			AttributeCoreID:    {IntValue: new(int64(cpu.CoreID))},
			AttributeCPUID:     {IntValue: new(int64(cpu.CpuID))},
			AttributeSMTLevel:  {IntValue: new(int64(topo.SMTLevel))},
		}

		addCompatibilityAttributes(deviceAttrs, int64(cpu.NUMANodeID), topo.SMTEnabled)
		if err := addPCIeRootsAttribute(pcieRootMapper, deviceAttrs, cpu.CpuID); err != nil {
			return nil, err
		}

		cpuDevice := resourceapi.Device{
			Name:                     deviceInfo.name,
			Attributes:               deviceAttrs,
			Capacity:                 make(map[resourceapi.QualifiedName]resourceapi.DeviceCapacity),
			NodeAllocatableResources: individualCPUNodeAllocatable(nodeAllocatableResources),
		}
		allDevices = append(allDevices, cpuDevice)
	}
	return allDevices, nil
}

func addPCIeRootsAttribute(pcieRootMapper *store.PCIeRootMapper, attrs map[resourceapi.QualifiedName]resourceapi.DeviceAttribute, cpuIDs ...int) error {
	if len(cpuIDs) == 0 {
		return nil // nothing to do
	}
	// Note: union semantics are correct because kernel cpulistaffinity currently collapses to NUMA granularity;
	// grouped allocation at socket/NUMA level therefore covers all CPUs local to every reported root.
	// See docs/dev/topology-linux-sysfs.md for in-depth exploration about the topic.
	pcieRoots := pcieRootMapper.GetPCIeRootsForCPU(cpuIDs...)
	if len(pcieRoots) == 0 {
		return nil // nothing to do
	}
	if len(pcieRoots) > resourceapi.DeviceAttributeMaxValueLength {
		return fmt.Errorf("PCIe roots %q cannot be represented within the limit of DRA max value length=%d", pcieRoots, resourceapi.DeviceAttributeMaxValueLength)
	}
	attrs[deviceattribute.StandardDeviceAttributePCIeRoot] = resourceapi.DeviceAttribute{StringValues: pcieRoots}
	return nil
}

func addCPUIDsAttribute(attrs map[resourceapi.QualifiedName]resourceapi.DeviceAttribute, cpus cpuset.CPUSet) error {
	if cpus.Size() == 0 {
		return nil // nothing to do
	}
	cpuIDs := cpus.String()
	if len(cpuIDs) > resourceapi.DeviceAttributeMaxValueLength {
		return fmt.Errorf("cpus %q cannot be represented within the limit of DRA max value length=%d", cpus.String(), resourceapi.DeviceAttributeMaxValueLength)
	}
	attrs[AttributeCPUIDs] = resourceapi.DeviceAttribute{StringValue: &cpuIDs}
	return nil
}

func addSMTMapAttribute(attrs map[resourceapi.QualifiedName]resourceapi.DeviceAttribute, topo *cpuinfo.CPUTopology) error {
	smtMap := FormatSMTMap(topo)
	if len(smtMap) == 0 {
		return fmt.Errorf("SMT map unexpectedly empty")
	}
	if len(smtMap) > resourceapi.DeviceAttributeMaxValueLength {
		return fmt.Errorf("SMT map %q cannot be represented within the limit of DRA max value length=%d", smtMap, resourceapi.DeviceAttributeMaxValueLength)
	}
	attrs[AttributeSMTMap] = resourceapi.DeviceAttribute{StringValue: new(smtMap)}
	return nil
}

func cpuSMTStride(info cpuinfo.CPUInfo) int {
	if info.SiblingCPUID == -1 || info.CpuID == -1 {
		return 0
	}
	if info.SiblingCPUID > info.CpuID {
		return info.SiblingCPUID - info.CpuID
	}
	return info.CpuID - info.SiblingCPUID
}
