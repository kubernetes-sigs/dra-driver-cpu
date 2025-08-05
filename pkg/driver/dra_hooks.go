/*
Copyright 2024 The Kubernetes Authors.

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
	"sort"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	resourceapi "k8s.io/api/resource/v1beta1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	"k8s.io/klog/v2"
	"k8s.io/utils/cpuset"
	cdiparser "tags.cncf.io/container-device-interface/pkg/parser"
)

const maxDevicesPerSlice = 128

// When true, hyperthreads of the same physical core are assigned consecutive device IDs.
// This allows the DRA scheduler, which requests resources in contiguous blocks, to co-locate workloads on hyperthreads of the same core.
const setConsecutiveDeviceIdToHyperthreads = true

// CreateCPUDeviceSlices creates Device objects based on the CPU topology.
func (cp *CPUDriver) CreateCPUDeviceSlices() [][]resourceapi.Device {
	cpus, err := cpuinfo.GetCPUInfos()
	if err != nil {
		klog.Errorf("error getting CPU topology: %v", err)
		return nil
	}

	processedCpus := make(map[int]bool)
	var coreGroups [][]cpuinfo.CPUInfo
	if setConsecutiveDeviceIdToHyperthreads {
		cpuInfoMap := make(map[int]cpuinfo.CPUInfo)
		for _, info := range cpus {
			cpuInfoMap[info.CpuID] = info
		}

		for _, cpu := range cpus {
			if processedCpus[cpu.CpuID] {
				continue
			}
			if cpu.SiblingCpuID != -1 {
				coreGroups = append(coreGroups, []cpuinfo.CPUInfo{cpu, cpuInfoMap[cpu.SiblingCpuID]})
				processedCpus[cpu.CpuID] = true
				processedCpus[cpu.SiblingCpuID] = true
			} else {
				coreGroups = append(coreGroups, []cpuinfo.CPUInfo{cpu})
				processedCpus[cpu.CpuID] = true
			}
		}
	} else {
		for _, cpu := range cpus {
			coreGroups = append(coreGroups, []cpuinfo.CPUInfo{cpu})
		}
	}

	sort.Slice(coreGroups, func(i, j int) bool {
		return coreGroups[i][0].CpuID < coreGroups[j][0].CpuID
	})

	devId := 0
	devicesByNuma := make(map[int64][]resourceapi.Device)
	for _, group := range coreGroups {
		for _, cpu := range group {
			numaNode := int64(cpu.NumaNode)
			l3CacheID := int64(cpu.L3CacheID)
			socketID := int64(cpu.SocketID)
			deviceName := fmt.Sprintf("cpudev%d", devId)
			devId++
			cp.cpuIDToDeviceName[cpu.CpuID] = deviceName
			cp.deviceNameToCPUID[deviceName] = cpu.CpuID
			cpuDevice := resourceapi.Device{
				Name: deviceName,
				Basic: &resourceapi.BasicDevice{
					Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
						"dra.cpu/numaNode":  {IntValue: &numaNode},
						"dra.cpu/l3CacheID": {IntValue: &l3CacheID},
						"dra.cpu/coreType":  {StringValue: &cpu.CoreType},
						"dra.cpu/socketID":  {IntValue: &socketID},
						// TODO(pravk03): Remove. Hack to align with NIC (DRANet). We need some standard attribute to align other resources with CPU.
						"dra.net/numaNode": {IntValue: &numaNode},
					},
					Capacity: make(map[resourceapi.QualifiedName]resourceapi.DeviceCapacity),
				},
			}
			devicesByNuma[numaNode] = append(devicesByNuma[numaNode], cpuDevice)
		}
	}

	numDevices := len(cpus)
	if numDevices <= maxDevicesPerSlice {
		klog.Infof("Total devices (%d) is within limit (%d), creating a single resource slice.", numDevices, maxDevicesPerSlice)
		var allDevices []resourceapi.Device

		// Sort NUMA node IDs to ensure a deterministic flattening order.
		numaNodeIDs := make([]int, 0, len(devicesByNuma))
		for id := range devicesByNuma {
			numaNodeIDs = append(numaNodeIDs, int(id))
		}
		sort.Ints(numaNodeIDs)

		// Flatten the map's values into a single slice.
		for _, id := range numaNodeIDs {
			allDevices = append(allDevices, devicesByNuma[int64(id)]...)
		}

		if len(allDevices) == 0 {
			return nil
		}
		return [][]resourceapi.Device{allDevices}
	} else {
		klog.Infof("Total devices (%d) exceeds limit (%d), creating one resource slice per NUMA node.", numDevices, maxDevicesPerSlice)
		// Create one resource slice per NUMA node.
		var allSlices [][]resourceapi.Device

		// Sort NUMA node IDs to ensure a deterministic slice ordering.
		numaNodeIDs := make([]int, 0, len(devicesByNuma))
		for id := range devicesByNuma {
			numaNodeIDs = append(numaNodeIDs, int(id))
		}
		sort.Ints(numaNodeIDs)

		for _, id := range numaNodeIDs {
			numaDevices := devicesByNuma[int64(id)]
			// If devices per NUMA node exceeds the limit, throw an error.
			if len(numaDevices) > maxDevicesPerSlice {
				klog.Errorf("number of devices for NUMA node %d (%d) exceeds the slice limit of %d", id, len(numaDevices), maxDevicesPerSlice)
				return nil
			}
			if len(numaDevices) > 0 {
				allSlices = append(allSlices, numaDevices)
			}
		}
		return allSlices
	}
}

// PublishResources publishes ResourceSlice for CPU resources.
func (cp *CPUDriver) PublishResources(ctx context.Context) {
	klog.Infof("Publishing resources")

	deviceChunks := cp.CreateCPUDeviceSlices()
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
		result[claim.UID] = cp.prepareResourceClaim(ctx, claim)
	}
	return result, nil
}

func getCDIDeviceName(uid types.UID) string {
	return fmt.Sprintf("claim-%s", uid)
}

func (cp *CPUDriver) prepareResourceClaim(_ context.Context, claim *resourceapi.ResourceClaim) kubeletplugin.PrepareResult {
	klog.Infof("prepareResourceClaim claim:%s/%s", claim.Namespace, claim.Name)

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
	claimCPUSet := cpuset.New(claimCPUIDs...)

	if claimCPUSet.IsEmpty() {
		klog.Errorf("prepareResourceClaim claim:%s/%s has no CPU allocations", claim.Namespace, claim.Name)
		return kubeletplugin.PrepareResult{}
	}

	deviceName := getCDIDeviceName(claim.UID)
	envVar := fmt.Sprintf("%s_claim_%s=%s", cdiEnvVarPrefix, claim.Name, claimCPUSet.String())
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
func (np *CPUDriver) UnprepareResourceClaims(ctx context.Context, claims []kubeletplugin.NamespacedObject) (map[types.UID]error, error) {
	klog.Infof("UnprepareResourceClaims is called: number of claims: %d", len(claims))

	result := make(map[types.UID]error)

	if len(claims) == 0 {
		return result, nil
	}

	for _, claim := range claims {
		klog.Infof("UnprepareResourceClaims claim:%+v", claim)
		err := np.unprepareResourceClaim(ctx, claim)
		result[claim.UID] = err
		if err != nil {
			klog.Infof("error unpreparing resources for claim %s/%s : %v", claim.Namespace, claim.Name, err)
		}
	}
	return result, nil
}

func (cp *CPUDriver) unprepareResourceClaim(_ context.Context, claim kubeletplugin.NamespacedObject) error {
	// Remove the device from the CDI spec file using the manager.
	return cp.cdiMgr.RemoveDevice(getCDIDeviceName(claim.UID))
}
