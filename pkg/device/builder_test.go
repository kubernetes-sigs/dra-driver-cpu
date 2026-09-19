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

package device_test

import (
	"fmt"
	"path"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	"github.com/stretchr/testify/require"
	v1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/utils/cpuset"
)

// fakeTopology returns a 4-CPU topology: CPUs 0,1 on socket0/NUMA0 and CPUs 2,3
// on socket1/NUMA1, SMT disabled.
func fakeTopology() *cpuinfo.CPUTopology {
	details := cpuinfo.CPUDetails{}
	for cpu := range 4 {
		socket := cpu / 2
		details[cpu] = cpuinfo.CPUInfo{
			CpuID:          cpu,
			CoreID:         cpu,
			SocketID:       socket,
			NUMANodeID:     socket,
			NumaNodeCPUSet: cpuset.New(socket*2, socket*2+1),
			SiblingCPUID:   -1,
		}
	}
	return &cpuinfo.CPUTopology{
		NumCPUs: 4, NumCores: 4, NumSockets: 2, NumNUMANodes: 2,
		SMTEnabled: false, CPUDetails: details,
	}
}

func TestDeviceBuilderNodeAllocatableResourceMapping(t *testing.T) {
	topo := fakeTopology()
	reserved := cpuset.New(0)
	one := resource.MustParse("1")

	tests := []struct {
		name                          string
		cpuDeviceMode                 string
		groupBy                       string
		publishNodeAllocatableMapping bool
	}{
		{
			name:                          "grouped/enabled",
			cpuDeviceMode:                 device.CPU_DEVICE_MODE_GROUPED,
			groupBy:                       device.GROUP_BY_NUMA_NODE,
			publishNodeAllocatableMapping: true,
		},
		{
			name:                          "grouped/disabled",
			cpuDeviceMode:                 device.CPU_DEVICE_MODE_GROUPED,
			groupBy:                       device.GROUP_BY_NUMA_NODE,
			publishNodeAllocatableMapping: false,
		},
		{
			name:                          "individual/enabled",
			cpuDeviceMode:                 device.CPU_DEVICE_MODE_INDIVIDUAL,
			publishNodeAllocatableMapping: true,
		},
		{
			name:                          "individual/disabled",
			cpuDeviceMode:                 device.CPU_DEVICE_MODE_INDIVIDUAL,
			publishNodeAllocatableMapping: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := device.Build(device.BuildInput{
				Inventory: device.Inventory{
					CPUTopology:  topo,
					ReservedCPUs: reserved,
				},
				Layout:                                device.FindLayout(tc.cpuDeviceMode, tc.groupBy),
				PCIeRootMapper:                        store.NewPCIeRootMapper(),
				PublishNodeAllocatableResourceMapping: tc.publishNodeAllocatableMapping,
			})
			require.NoError(t, err)
			require.NotEmpty(t, res.Devices)

			for _, dev := range res.Devices {
				if !tc.publishNodeAllocatableMapping {
					require.Nil(t, dev.NodeAllocatableResources,
						"device %q must not expose nodeAllocatableResources when publishing is disabled", dev.Name)
					continue
				}

				require.Contains(t, dev.NodeAllocatableResources, v1.ResourceCPU,
					"device %q must expose a node allocatable mapping for cpu", dev.Name)
				nar := dev.NodeAllocatableResources[v1.ResourceCPU]
				require.NotNil(t, nar.Mapping, "device %q: mapping must be set", dev.Name)
				require.Nil(t, nar.Overhead, "device %q: overhead must not be set", dev.Name)

				if tc.cpuDeviceMode == device.CPU_DEVICE_MODE_GROUPED {
					// Grouped devices expose consumable capacity: the mapping must reference
					// the dra.cpu/cpu capacity with a 1:1 multiplier. The capacityKey must
					// reference an existing capacity entry or the apiserver rejects the slice.
					require.NotNil(t, nar.Mapping.CapacityKey, "device %q: capacityKey must be set", dev.Name)
					require.Equal(t, resourceapi.QualifiedName(device.CPUResourceQualifiedName), *nar.Mapping.CapacityKey)
					require.NotNil(t, nar.Mapping.CapacityMultiplier, "device %q: capacityMultiplier must be set", dev.Name)
					require.Zero(t, nar.Mapping.CapacityMultiplier.Cmp(one), "device %q: capacityMultiplier must be 1", dev.Name)
					require.Nil(t, nar.Mapping.DeviceMultiplier,
						"device %q: deviceMultiplier is mutually exclusive with capacityKey", dev.Name)
					require.Contains(t, dev.Capacity, resourceapi.QualifiedName(device.CPUResourceQualifiedName),
						"device %q: capacityKey must reference a defined capacity", dev.Name)
				} else {
					// Individual devices are one CPU each: the mapping must use a
					// deviceMultiplier of 1.
					require.NotNil(t, nar.Mapping.DeviceMultiplier, "device %q: deviceMultiplier must be set", dev.Name)
					require.Zero(t, nar.Mapping.DeviceMultiplier.Cmp(one), "device %q: deviceMultiplier must be 1", dev.Name)
					require.Nil(t, nar.Mapping.CapacityKey,
						"device %q: capacityKey is mutually exclusive with deviceMultiplier", dev.Name)
					require.Nil(t, nar.Mapping.CapacityMultiplier,
						"device %q: capacityMultiplier is only valid with capacityKey", dev.Name)
				}
			}
		})
	}
}

func TestMachineGroupedUsesTopologyValidatedCPUs(t *testing.T) {
	topo := fakeTopology()
	// CPU 4 was omitted from CPUDetails because topology discovery could not
	// validate it.

	res, err := device.Build(
		device.BuildInput{
			Inventory: device.Inventory{
				CPUTopology:  topo,
				ReservedCPUs: cpuset.New(),
			},
			Layout:                                device.LayoutMachine,
			PCIeRootMapper:                        store.NewPCIeRootMapper(),
			PublishNodeAllocatableResourceMapping: false,
		})
	require.NoError(t, err)
	require.Len(t, res.Devices, 1)

	capacity := res.Devices[0].Capacity[resourceapi.QualifiedName(device.CPUResourceQualifiedName)]
	require.Equal(t, int64(4), capacity.Value.Value())
	numCPUs := res.Devices[0].Attributes[device.AttributeNumCPUs]
	require.NotNil(t, numCPUs.IntValue)
	require.Equal(t, int64(4), *numCPUs.IntValue)
}

// TestBuildPublishesScalarNUMANodeAttribute pins the shape of the standard
// numaNode attribute published for a device bound to a single NUMA node: the
// standard name, the scalar int type and the value. The value is built by the
// upstream deviceattribute helpers, but the published attribute must remain
// exactly what the driver published before the switch.
func TestBuildPublishesScalarNUMANodeAttribute(t *testing.T) {
	tests := []struct {
		name     string
		layout   device.Layout
		wantNUMA map[string]int64
	}{
		{
			name:   "individual",
			layout: device.LayoutIndividual,
			wantNUMA: map[string]int64{
				"cpudev000": 0,
				"cpudev001": 0,
				"cpudev002": 1,
				"cpudev003": 1,
			},
		},
		{
			name:   "grouped by numanode",
			layout: device.LayoutNUMANode,
			wantNUMA: map[string]int64{
				"cpudevnuma000": 0,
				"cpudevnuma001": 1,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := device.Build(device.BuildInput{
				Inventory: device.Inventory{
					CPUTopology:  fakeTopology(),
					ReservedCPUs: cpuset.New(),
				},
				Layout:         tc.layout,
				PCIeRootMapper: store.NewPCIeRootMapper(),
			})
			require.NoError(t, err)
			require.Len(t, res.Devices, len(tc.wantNUMA))

			for _, dev := range res.Devices {
				want, found := tc.wantNUMA[dev.Name]
				require.Truef(t, found, "unexpected device %q", dev.Name)

				attr, found := dev.Attributes[deviceattribute.StandardDeviceAttributeNUMANode]
				require.Truef(t, found, "device %q must publish the standard numaNode attribute", dev.Name)
				// This release publishes the scalar form only; the list form is a
				// separate follow-up (see #320).
				require.Nilf(t, attr.IntValues, "device %q must publish numaNode in scalar form", dev.Name)
				require.NotNilf(t, attr.IntValue, "device %q must publish a numaNode int value", dev.Name)
				require.Equalf(t, want, *attr.IntValue, "device %q numaNode", dev.Name)
			}
		})
	}
}

func TestBuildValidatesDeviceAttributeValueCount(t *testing.T) {
	topo := &cpuinfo.CPUTopology{
		NumCPUs: 1,
		CPUDetails: cpuinfo.CPUDetails{
			0: {CpuID: 0, CoreID: 0, SocketID: 0, NUMANodeID: 0, SiblingCPUID: -1},
		},
	}
	build := func(mapper *store.PCIeRootMapper) (device.BuildResult, error) {
		return device.Build(device.BuildInput{
			Inventory: device.Inventory{
				CPUTopology:  topo,
				ReservedCPUs: cpuset.New(),
			},
			Layout:         device.LayoutNUMANode,
			PCIeRootMapper: mapper,
			ExposeExtAttrs: true,
		})
	}

	withoutPCIeRoots, err := build(store.NewPCIeRootMapper())
	require.NoError(t, err)
	maxPCIeRoots := resourceapi.ResourceSliceMaxAttributeValuesPerDevice - countAttributeValues(withoutPCIeRoots.Devices[0].Attributes)

	withPCIeRoots, err := build(newPCIeRootMapper(t, maxPCIeRoots))
	require.NoError(t, err)
	device := withPCIeRoots.Devices[0]
	require.Len(t, device.Attributes[deviceattribute.StandardDeviceAttributePCIeRoot].StringValues, maxPCIeRoots)
	require.LessOrEqual(t, len(device.Attributes)+len(device.Capacity), resourceapi.ResourceSliceMaxAttributesAndCapacitiesPerDevice)
	require.LessOrEqual(t, countAttributeValues(device.Attributes), resourceapi.ResourceSliceMaxAttributeValuesPerDevice)

	_, err = build(newPCIeRootMapper(t, maxPCIeRoots+1))
	require.ErrorContains(t, err, "DRA max attribute value limit")
}

func newPCIeRootMapper(t *testing.T, rootCount int) *store.PCIeRootMapper {
	t.Helper()

	sysfs := fstest.MapFS{}
	for i := range rootCount {
		rootName := fmt.Sprintf("pci%04x:%02x", i/256, i%256)
		busID := strings.TrimPrefix(rootName, "pci")
		sysfs[path.Join("devices", rootName, "pci_bus", busID, "cpulistaffinity")] = &fstest.MapFile{Data: []byte("0\n")}
	}

	mapper := store.NewPCIeRootMapper()
	require.NoError(t, mapper.Probe(testr.New(t), sysfs, cpuset.New(0)))
	return mapper
}

func countAttributeValues(attrs map[resourceapi.QualifiedName]resourceapi.DeviceAttribute) int {
	count := 0
	for _, attr := range attrs {
		if len(attr.BoolValues) == 0 && len(attr.IntValues) == 0 && len(attr.StringValues) == 0 && len(attr.VersionValues) == 0 {
			count++
			continue
		}
		count += len(attr.BoolValues) + len(attr.IntValues) + len(attr.StringValues) + len(attr.VersionValues)
	}
	return count
}
