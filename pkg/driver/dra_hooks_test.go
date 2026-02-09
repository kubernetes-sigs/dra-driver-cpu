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
	"strings"
	"testing"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	"k8s.io/utils/cpuset"
	cdiparser "tags.cncf.io/container-device-interface/pkg/parser"
)

const (
	testNodeName   = "test-node"
	testDriverName = "dra-driver-cpu.k8s.io"
)

type mockKubeletPlugin struct {
	publishedResources *resourceslice.DriverResources
	publishError       error
}

func (m *mockKubeletPlugin) PublishResources(ctx context.Context, resources resourceslice.DriverResources) error {
	m.publishedResources = &resources
	if m.publishError != nil {
		return m.publishError
	}
	return nil
}

func (m *mockKubeletPlugin) Stop() {}

type mockCdiMgr struct {
	devices     map[string]string
	addError    error
	removeError error
}

func newMockCdiMgr() *mockCdiMgr {
	return &mockCdiMgr{
		devices: make(map[string]string),
	}
}

func (m *mockCdiMgr) AddDevice(deviceName, envVar string) error {
	if m.addError != nil {
		return m.addError
	}
	m.devices[deviceName] = envVar
	return nil
}

func (m *mockCdiMgr) RemoveDevice(deviceName string) error {
	if m.removeError != nil {
		return m.removeError
	}
	delete(m.devices, deviceName)
	return nil
}

var (
	// Sibling CPUs are non-consecutive: (0,2), (1,3)
	mockCPUInfos_SingleSocket_4CPUS_HT = []cpuinfo.CPUInfo{
		{CpuID: 0, CoreID: 0, SocketID: 0, NUMANodeID: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 2},
		{CpuID: 1, CoreID: 1, SocketID: 0, NUMANodeID: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 3},
		{CpuID: 2, CoreID: 0, SocketID: 0, NUMANodeID: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 0},
		{CpuID: 3, CoreID: 1, SocketID: 0, NUMANodeID: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 1},
	}
	mockCPUInfos_SingleSocket_4CPUs_HT_Off = []cpuinfo.CPUInfo{
		{CpuID: 0, CoreID: 0, SocketID: 0, NUMANodeID: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: -1},
		{CpuID: 1, CoreID: 1, SocketID: 0, NUMANodeID: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: -1},
		{CpuID: 2, CoreID: 2, SocketID: 0, NUMANodeID: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: -1},
		{CpuID: 3, CoreID: 3, SocketID: 0, NUMANodeID: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: -1},
	}
	// P-core sibling is non-consecutive: (0,2)
	mockCPUInfos_SingleSocket_Hybrid_HT = []cpuinfo.CPUInfo{
		{CpuID: 0, CoreID: 0, SocketID: 0, NUMANodeID: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 2},
		{CpuID: 1, CoreID: 1, SocketID: 0, NUMANodeID: 0, CoreType: cpuinfo.CoreTypeEfficiency, SiblingCpuID: 3},
		{CpuID: 2, CoreID: 0, SocketID: 0, NUMANodeID: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 0},
		{CpuID: 3, CoreID: 1, SocketID: 0, NUMANodeID: 0, CoreType: cpuinfo.CoreTypeEfficiency, SiblingCpuID: 1},
	}
	// 2 sockets, 2 cores/socket, HT on. Total 8 logical CPUs.
	mockCPUInfos_DualSocket_4CPUsPerSocket_HT = []cpuinfo.CPUInfo{
		{CpuID: 0, CoreID: 0, SocketID: 0, NUMANodeID: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 4},
		{CpuID: 1, CoreID: 1, SocketID: 0, NUMANodeID: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 5},
		{CpuID: 2, CoreID: 2, SocketID: 1, NUMANodeID: 1, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 6},
		{CpuID: 3, CoreID: 3, SocketID: 1, NUMANodeID: 1, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 7},
		{CpuID: 4, CoreID: 0, SocketID: 0, NUMANodeID: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 0},
		{CpuID: 5, CoreID: 1, SocketID: 0, NUMANodeID: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 1},
		{CpuID: 6, CoreID: 2, SocketID: 1, NUMANodeID: 1, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 2},
		{CpuID: 7, CoreID: 3, SocketID: 1, NUMANodeID: 1, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 3},
	}
	mockCPUInfos_SingleNUMANodeExceedsSliceLimit = func() []cpuinfo.CPUInfo {
		var infos []cpuinfo.CPUInfo
		for i := 0; i < maxDevicesPerResourceSlice+1; i++ {
			infos = append(infos, cpuinfo.CPUInfo{CpuID: i, CoreID: i, SocketID: 0, NUMANodeID: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: -1})
		}
		return infos
	}()
	mockCPUInfos_DualSocket_120CPUsPerSocket_HT = func() []cpuinfo.CPUInfo {
		var infos []cpuinfo.CPUInfo
		numCores := 120
		for socketID := 0; socketID < 2; socketID++ {
			for coreID := 0; coreID < numCores/2; coreID++ {
				baseCpuID := socketID * numCores
				infos = append(infos, cpuinfo.CPUInfo{
					CpuID:        baseCpuID + coreID*2,
					CoreID:       coreID,
					SocketID:     socketID,
					NUMANodeID:   socketID,
					CoreType:     cpuinfo.CoreTypePerformance,
					SiblingCpuID: baseCpuID + coreID*2 + 1,
				})

				// Create the second logical CPU (thread 1) on the same core
				infos = append(infos, cpuinfo.CPUInfo{
					CpuID:        baseCpuID + coreID*2 + 1,
					CoreID:       coreID,
					SocketID:     socketID,
					NUMANodeID:   socketID,
					CoreType:     cpuinfo.CoreTypePerformance,
					SiblingCpuID: baseCpuID + coreID*2,
				})
			}
		}
		return infos
	}()
)

func TestPublishResources(t *testing.T) {
	testCases := []struct {
		name                       string
		cpuInfos                   []cpuinfo.CPUInfo
		cpuInfoErr                 error
		publishError               error
		reservedCPUs               cpuset.CPUSet
		expectPublish              bool
		expectedNumSlices          int
		expectedDevices            int
		expectedDevicesPerNUMANode map[int]int
	}{
		{
			name:                       "single socket, HT on",
			cpuInfos:                   mockCPUInfos_SingleSocket_4CPUS_HT,
			reservedCPUs:               cpuset.New(),
			expectPublish:              true,
			expectedNumSlices:          1,
			expectedDevices:            len(mockCPUInfos_SingleSocket_4CPUS_HT),
			expectedDevicesPerNUMANode: map[int]int{0: 4},
		},
		{
			name:                       "dual socket, HT on, 1 CPU reserved",
			cpuInfos:                   mockCPUInfos_DualSocket_4CPUsPerSocket_HT,
			reservedCPUs:               cpuset.New(0),
			expectPublish:              true,
			expectedNumSlices:          1, // 1 slice with CPUs from all the NUMA nodes
			expectedDevices:            len(mockCPUInfos_DualSocket_4CPUsPerSocket_HT) - 1,
			expectedDevicesPerNUMANode: map[int]int{0: 3, 1: 4},
		},
		{
			name:                       "single socket, HT off",
			cpuInfos:                   mockCPUInfos_SingleSocket_4CPUs_HT_Off,
			reservedCPUs:               cpuset.New(),
			expectPublish:              true,
			expectedNumSlices:          1,
			expectedDevices:            len(mockCPUInfos_SingleSocket_4CPUs_HT_Off),
			expectedDevicesPerNUMANode: map[int]int{0: 4},
		},
		{
			name:                       "single socket, hybrid",
			cpuInfos:                   mockCPUInfos_SingleSocket_Hybrid_HT,
			reservedCPUs:               cpuset.New(),
			expectPublish:              true,
			expectedNumSlices:          1,
			expectedDevices:            len(mockCPUInfos_SingleSocket_Hybrid_HT),
			expectedDevicesPerNUMANode: map[int]int{0: 4},
		},
		{
			name:                       "dual socket, 4 CPUs per socker, HT on",
			cpuInfos:                   mockCPUInfos_DualSocket_4CPUsPerSocket_HT,
			reservedCPUs:               cpuset.New(),
			expectPublish:              true,
			expectedNumSlices:          1, // We should create just one slice with all cpus from both the NUMA nodes.
			expectedDevices:            len(mockCPUInfos_DualSocket_4CPUsPerSocket_HT),
			expectedDevicesPerNUMANode: map[int]int{0: 4, 1: 4},
		},
		{
			name:                       "dual socket, 120 CPUs per socker, HT on",
			cpuInfos:                   mockCPUInfos_DualSocket_120CPUsPerSocket_HT,
			reservedCPUs:               cpuset.New(),
			expectPublish:              true,
			expectedNumSlices:          2, // We should create 2 slices as number of CPUs on the machine exceeds 128.
			expectedDevices:            len(mockCPUInfos_DualSocket_120CPUsPerSocket_HT),
			expectedDevicesPerNUMANode: map[int]int{0: 120, 1: 120},
		},
		{
			name:          "no devices to publish",
			cpuInfos:      []cpuinfo.CPUInfo{},
			reservedCPUs:  cpuset.New(),
			expectPublish: false,
		},
		{
			name:          "error getting cpu info",
			cpuInfoErr:    fmt.Errorf("cpuinfo error"),
			reservedCPUs:  cpuset.New(),
			expectPublish: false,
		},
		{
			name:              "error publishing",
			cpuInfos:          mockCPUInfos_SingleSocket_4CPUS_HT,
			publishError:      fmt.Errorf("publish error"),
			reservedCPUs:      cpuset.New(),
			expectPublish:     true,
			expectedNumSlices: 1,
			expectedDevices:   len(mockCPUInfos_SingleSocket_4CPUS_HT),
		},
		{
			name:              "error because devices on one NUMA > maxDevicesPerResourceSlice",
			cpuInfos:          mockCPUInfos_SingleNUMANodeExceedsSliceLimit,
			reservedCPUs:      cpuset.New(),
			expectPublish:     false,
			expectedNumSlices: 0,
			expectedDevices:   0,
		},
		{
			name:                       "publish with reserved cpus",
			cpuInfos:                   mockCPUInfos_SingleSocket_4CPUS_HT,
			reservedCPUs:               cpuset.New(0, 1),
			expectPublish:              true,
			expectedNumSlices:          1,
			expectedDevices:            len(mockCPUInfos_SingleSocket_4CPUS_HT) - 2,
			expectedDevicesPerNUMANode: map[int]int{0: 2},
		},
		{
			name:                       "all cpus reserved",
			cpuInfos:                   mockCPUInfos_SingleSocket_4CPUS_HT,
			reservedCPUs:               cpuset.New(0, 1, 2, 3),
			expectPublish:              false,
			expectedNumSlices:          0, // No cpus to publish in ResourceSlice
			expectedDevices:            0,
			expectedDevicesPerNUMANode: map[int]int{},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockPlugin := &mockKubeletPlugin{publishError: tc.publishError}
			mockProvider := &cpuinfo.MockCPUInfoProvider{CPUInfos: tc.cpuInfos, Err: tc.cpuInfoErr}
			topo, _ := mockProvider.GetCPUTopology()
			cp := &CPUDriver{
				nodeName:          testNodeName,
				draPlugin:         mockPlugin,
				deviceNameToCPUID: make(map[string]int),
				cpuTopology:       topo,
				reservedCPUs:      tc.reservedCPUs,
			}

			cp.PublishResources(context.Background())

			if !tc.expectPublish {
				require.Nil(t, mockPlugin.publishedResources)
				return
			}

			// The driver should attempt to publish resources even if the plugin will error.
			require.NotNil(t, mockPlugin.publishedResources)

			if tc.publishError != nil {
				return // Further validation is not needed if publishing failed.
			}

			if tc.expectedNumSlices == 0 {
				if tc.cpuInfoErr == nil {
					require.Len(t, mockPlugin.publishedResources.Pools, 0)
				}
				return
			}

			require.Len(t, mockPlugin.publishedResources.Pools, 1)
			pool, ok := mockPlugin.publishedResources.Pools[testNodeName]
			require.True(t, ok)
			require.Len(t, pool.Slices, tc.expectedNumSlices)

			totalDevices := 0
			for _, s := range pool.Slices {
				totalDevices += len(s.Devices)
			}
			require.Equal(t, tc.expectedDevices, totalDevices)
			require.Equal(t, tc.expectedDevices, len(cp.deviceNameToCPUID))

			// Verify device attributes
			cpuInfosMap := make(map[int]cpuinfo.CPUInfo)
			for _, info := range tc.cpuInfos {
				cpuInfosMap[info.CpuID] = info
			}
			devicesPerNumaInSlices := make(map[int]int)
			seenCPUIDs := make(map[int]bool)
			for _, slice := range pool.Slices {
				// If we expect more than one slice, all devices in a slice should belong to the same NUMA node.
				if tc.expectedNumSlices > 1 {
					numaNode := *slice.Devices[0].Attributes["dra.cpu/numaNodeID"].IntValue
					for _, device := range slice.Devices {
						require.Equal(t, numaNode, *device.Attributes["dra.cpu/numaNodeID"].IntValue)
					}
				}

				for _, device := range slice.Devices {
					cpuID, ok := cp.deviceNameToCPUID[device.Name]
					require.True(t, ok)
					require.False(t, seenCPUIDs[cpuID], "duplicate cpuID found in slices: %d", cpuID)
					seenCPUIDs[cpuID] = true

					var cpuInfo *cpuinfo.CPUInfo
					for i := range tc.cpuInfos {
						if tc.cpuInfos[i].CpuID == cpuID {
							cpuInfo = &tc.cpuInfos[i]
							break
						}
					}
					require.NotNil(t, cpuInfo, "could not find matching cpuInfo for device %s", device.Name)

					numaNode := int64(cpuInfo.NUMANodeID)
					CacheL3ID := int64(cpuInfo.UncoreCacheID)
					coreType := cpuInfo.CoreType.String()
					socketID := int64(cpuInfo.SocketID)

					require.Equal(t, numaNode, *device.Attributes["dra.cpu/numaNodeID"].IntValue)
					require.Equal(t, CacheL3ID, *device.Attributes["dra.cpu/cacheL3ID"].IntValue)
					require.Equal(t, coreType, *device.Attributes["dra.cpu/coreType"].StringValue)
					require.Equal(t, socketID, *device.Attributes["dra.cpu/socketID"].IntValue)
					devicesPerNumaInSlices[cpuInfo.NUMANodeID]++
				}
			}
			require.Equal(t, len(tc.expectedDevicesPerNUMANode), len(devicesPerNumaInSlices))
			for numaNode, numDevices := range tc.expectedDevicesPerNUMANode {
				require.Equal(t, numDevices, devicesPerNumaInSlices[numaNode], "mismatch in device count for numa node %d", numaNode)
			}

			// Test if hyperthreads are given successive device names
			cpuIDToDeviceName := make(map[int]string)
			for devName, cpuID := range cp.deviceNameToCPUID {
				cpuIDToDeviceName[cpuID] = devName
			}
			for _, info := range tc.cpuInfos {
				if info.SiblingCpuID == -1 || info.CpuID > info.SiblingCpuID {
					continue
				}

				if tc.reservedCPUs.Contains(info.CpuID) || tc.reservedCPUs.Contains(info.SiblingCpuID) {
					continue
				}

				deviceName1, ok := cpuIDToDeviceName[info.CpuID]
				require.True(t, ok, "device for cpuID %d not found in slices", info.CpuID)
				deviceName2, ok := cpuIDToDeviceName[info.SiblingCpuID]
				require.True(t, ok, "device for sibling cpuID %d not found in slices", info.SiblingCpuID)

				var devNum1, devNum2 int
				_, err := fmt.Sscanf(deviceName1, "cpudev%d", &devNum1)
				require.NoError(t, err)
				_, err = fmt.Sscanf(deviceName2, "cpudev%d", &devNum2)
				require.NoError(t, err)

				require.Equal(t, devNum1+1, devNum2, "hyperthread device names are not successive for core %d (cpus %d, %d)", info.CoreID, info.CpuID, info.SiblingCpuID)
			}
		})
	}
}

func TestPrepareResourceClaims(t *testing.T) {
	mockProvider := &cpuinfo.MockCPUInfoProvider{CPUInfos: mockCPUInfos_SingleSocket_4CPUS_HT}
	topo, _ := mockProvider.GetCPUTopology()
	baseCPUDriver := func() *CPUDriver {
		return &CPUDriver{
			driverName: testDriverName,
			deviceNameToCPUID: map[string]int{
				"cpudev0": 0,
				"cpudev1": 1,
			},
			cpuAllocationStore: store.NewCPUAllocation(topo, cpuset.New()),
		}
	}

	claimUID := types.UID("claim-1")
	cdiDeviceName := getCDIDeviceName(claimUID)
	cdiQualifiedName := cdiparser.QualifiedName(cdiVendor, cdiClass, cdiDeviceName)

	testCases := []struct {
		name                    string
		driver                  *CPUDriver
		claims                  []*resourceapi.ResourceClaim
		mockCdiAddError         error
		expectedResultsCount    int
		expectedError           bool
		expectedCdiDevicesCount int
		expectedCdiDevice       string
		expectedCdiEnvVar       string
		expectedPreparedDevices []kubeletplugin.Device
	}{
		{
			name:   "success",
			driver: baseCPUDriver(),
			claims: []*resourceapi.ResourceClaim{
				{
					ObjectMeta: metav1.ObjectMeta{UID: claimUID, Name: "my-claim"},
					Status: resourceapi.ResourceClaimStatus{
						Allocation: &resourceapi.AllocationResult{
							Devices: resourceapi.DeviceAllocationResult{
								Results: []resourceapi.DeviceRequestAllocationResult{
									{Driver: testDriverName, Pool: testNodeName, Device: "cpudev0"},
									{Driver: testDriverName, Pool: testNodeName, Device: "cpudev1"},
								},
							},
						},
					},
				},
			},
			expectedResultsCount:    1,
			expectedCdiDevicesCount: 1,
			expectedCdiDevice:       cdiDeviceName,
			expectedCdiEnvVar:       fmt.Sprintf("%s_%s=%s", cdiEnvVarPrefix, claimUID, "0-1"),
			expectedPreparedDevices: []kubeletplugin.Device{
				{PoolName: testNodeName, DeviceName: "cpudev0", CDIDeviceIDs: []string{cdiQualifiedName}},
				{PoolName: testNodeName, DeviceName: "cpudev1", CDIDeviceIDs: []string{cdiQualifiedName}},
			},
		},
		{
			name:                 "no claims",
			driver:               baseCPUDriver(),
			claims:               []*resourceapi.ResourceClaim{},
			expectedResultsCount: 0,
		},
		{
			name:   "claim with no allocation",
			driver: baseCPUDriver(),
			claims: []*resourceapi.ResourceClaim{
				{ObjectMeta: metav1.ObjectMeta{UID: "claim-no-alloc"}},
			},
			expectedResultsCount: 1,
			expectedError:        true,
		},
		{
			name:   "claim with device from other driver",
			driver: baseCPUDriver(),
			claims: []*resourceapi.ResourceClaim{
				{
					ObjectMeta: metav1.ObjectMeta{UID: "claim-other-driver"},
					Status: resourceapi.ResourceClaimStatus{
						Allocation: &resourceapi.AllocationResult{
							Devices: resourceapi.DeviceAllocationResult{
								Results: []resourceapi.DeviceRequestAllocationResult{
									{Driver: "other-driver", Device: "other-device"},
								},
							},
						},
					},
				},
			},
			expectedResultsCount:    1,
			expectedCdiDevicesCount: 0,
			expectedPreparedDevices: nil,
		},
		{
			name:   "error - device not found",
			driver: baseCPUDriver(),
			claims: []*resourceapi.ResourceClaim{
				{
					ObjectMeta: metav1.ObjectMeta{UID: "claim-dev-not-found"},
					Status: resourceapi.ResourceClaimStatus{
						Allocation: &resourceapi.AllocationResult{
							Devices: resourceapi.DeviceAllocationResult{
								Results: []resourceapi.DeviceRequestAllocationResult{
									{Driver: testDriverName, Device: "non-existent-device"},
								},
							},
						},
					},
				},
			},
			expectedResultsCount: 1,
			expectedError:        true,
		},
		{
			name:            "error - cdi add fails",
			driver:          baseCPUDriver(),
			mockCdiAddError: fmt.Errorf("cdi add error"),
			claims: []*resourceapi.ResourceClaim{
				{
					ObjectMeta: metav1.ObjectMeta{UID: "claim-cdi-fails"},
					Status: resourceapi.ResourceClaimStatus{
						Allocation: &resourceapi.AllocationResult{
							Devices: resourceapi.DeviceAllocationResult{
								Results: []resourceapi.DeviceRequestAllocationResult{
									{Driver: testDriverName, Device: "cpudev0"},
								},
							},
						},
					},
				},
			},
			expectedResultsCount: 1,
			expectedError:        true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockCdiMgr := newMockCdiMgr()
			mockCdiMgr.addError = tc.mockCdiAddError
			tc.driver.cdiMgr = mockCdiMgr

			preparedClaims, err := tc.driver.PrepareResourceClaims(context.Background(), tc.claims)
			require.NoError(t, err)
			require.Len(t, preparedClaims, tc.expectedResultsCount)

			if len(tc.claims) > 0 {
				claimUID := tc.claims[0].UID
				result := preparedClaims[claimUID]
				if tc.expectedError {
					require.Error(t, result.Err)
					require.Empty(t, result.Devices)
				} else {
					require.NoError(t, result.Err)
					require.ElementsMatch(t, tc.expectedPreparedDevices, result.Devices)
				}
			}

			require.Len(t, mockCdiMgr.devices, tc.expectedCdiDevicesCount)
			if tc.expectedCdiDevice != "" {
				envVar, ok := mockCdiMgr.devices[tc.expectedCdiDevice]
				require.True(t, ok, "expected CDI device not found")
				require.Equal(t, tc.expectedCdiEnvVar, envVar)
			}
		})
	}
}

func TestPrepareResourceClaimsGroupedMode(t *testing.T) {
	baseCPUDriver := func(groupBy string, cpuInfos []cpuinfo.CPUInfo, initialAllocations map[types.UID]cpuset.CPUSet, reservedCPUs cpuset.CPUSet) *CPUDriver {
		driver := &CPUDriver{}
		driver.driverName = testDriverName
		driver.cpuDeviceMode = CPU_DEVICE_MODE_GROUPED
		driver.cpuDeviceGroupBy = groupBy
		driver.deviceNameToSocketID = make(map[string]int)
		driver.deviceNameToNUMANodeID = make(map[string]int)
		mockProvider := &cpuinfo.MockCPUInfoProvider{CPUInfos: cpuInfos}
		driver.cpuTopology, _ = mockProvider.GetCPUTopology()
		driver.cpuAllocationStore = store.NewCPUAllocation(driver.cpuTopology, reservedCPUs)
		for claimUID, cpus := range initialAllocations {
			driver.cpuAllocationStore.AddResourceClaimAllocation(claimUID, cpus)
		}

		topo, err := mockProvider.GetCPUTopology()
		require.NoError(t, err) // We don't expect errors in test setup

		switch driver.cpuDeviceGroupBy {
		case GROUP_BY_SOCKET:
			for i := 0; i < topo.NumSockets; i++ {
				driver.deviceNameToSocketID[fmt.Sprintf("%s%d", cpuDeviceSocketGroupedPrefix, i)] = i
			}
		case GROUP_BY_NUMA_NODE:
			for i := 0; i < topo.NumNUMANodes; i++ {
				driver.deviceNameToNUMANodeID[fmt.Sprintf("%snuma%d", cpuDevicePrefix, i)] = i
			}
		}
		return driver
	}

	claimUID := types.UID("claim-1")
	cdiDeviceName := getCDIDeviceName(claimUID)
	cdiQualifiedName := cdiparser.QualifiedName(cdiVendor, cdiClass, cdiDeviceName)

	testCases := []struct {
		name                    string
		cpuInfos                []cpuinfo.CPUInfo
		groupBy                 string
		reservedCPUs            cpuset.CPUSet
		initialAllocations      map[types.UID]cpuset.CPUSet
		claims                  []*resourceapi.ResourceClaim
		mockCdiAddError         error
		expectedError           bool
		expectedPreparedDevices []kubeletplugin.Device
		expectedCPUSet          cpuset.CPUSet
	}{
		{
			name:     "SocketGrouped_TopoSingleSocketHT_Alloc2CPU",
			cpuInfos: mockCPUInfos_SingleSocket_4CPUS_HT,
			groupBy:  GROUP_BY_SOCKET,
			claims:   []*resourceapi.ResourceClaim{testClaim(claimUID, testDriverName, testNodeName, map[string]int64{"cpudevsocket0": 2})},
			// 2 hyperthreads from the same core is allocated
			expectedCPUSet: cpuset.New(0, 2),
		},
		{
			name:     "NUMAGrouped_TopoSingleSocketHT_Alloc2CPU",
			cpuInfos: mockCPUInfos_SingleSocket_4CPUS_HT,
			groupBy:  GROUP_BY_NUMA_NODE,
			claims:   []*resourceapi.ResourceClaim{testClaim(claimUID, testDriverName, testNodeName, map[string]int64{"cpudevnuma0": 2})},
			// 2 hyperthreads from the same core is allocated
			expectedCPUSet: cpuset.New(0, 2),
		},
		{
			name:     "SocketGrouped_DualSocketHT_Alloc4CPU",
			cpuInfos: mockCPUInfos_DualSocket_4CPUsPerSocket_HT,
			groupBy:  GROUP_BY_SOCKET,
			claims:   []*resourceapi.ResourceClaim{testClaim(claimUID, testDriverName, testNodeName, map[string]int64{"cpudevsocket0": 4})},
			// hyperthreads from the same core is allocated
			expectedCPUSet: cpuset.New(0, 4, 1, 5),
		},
		{
			name:           "NUMAGrouped_DualSocketHT_Alloc4CPUFromNUMANode1",
			cpuInfos:       mockCPUInfos_DualSocket_4CPUsPerSocket_HT,
			groupBy:        GROUP_BY_NUMA_NODE,
			claims:         []*resourceapi.ResourceClaim{testClaim(claimUID, testDriverName, testNodeName, map[string]int64{"cpudevnuma1": 4})},
			expectedCPUSet: cpuset.New(2, 6, 3, 7),
		},
		{
			name:         "SocketGrouped_DualSocketHT_Alloc2CPU_Socket0_WithReserved",
			cpuInfos:     mockCPUInfos_DualSocket_4CPUsPerSocket_HT,
			groupBy:      GROUP_BY_SOCKET,
			reservedCPUs: cpuset.New(0, 4),
			claims:       []*resourceapi.ResourceClaim{testClaim(claimUID, testDriverName, testNodeName, map[string]int64{"cpudevsocket0": 2})},
			// prefer hyperthreads on same core over lower numbered CPUs
			expectedCPUSet: cpuset.New(1, 5),
		},
		{
			name:         "NUMAGrouped_DualSocketHT_Alloc2CPU_NUMA1_WithReserved",
			cpuInfos:     mockCPUInfos_DualSocket_4CPUsPerSocket_HT,
			groupBy:      GROUP_BY_NUMA_NODE,
			reservedCPUs: cpuset.New(2, 6),
			claims:       []*resourceapi.ResourceClaim{testClaim(claimUID, testDriverName, testNodeName, map[string]int64{"cpudevnuma1": 2})},
			// prefer hyperthreads on same core over lower numbered CPUs
			expectedCPUSet: cpuset.New(3, 7),
			expectedError:  false,
		},
		{
			name:          "SocketGrouped_DualSocketHT_DeviceNotFound_Socket",
			cpuInfos:      mockCPUInfos_DualSocket_4CPUsPerSocket_HT,
			groupBy:       GROUP_BY_SOCKET,
			claims:        []*resourceapi.ResourceClaim{testClaim(claimUID, testDriverName, testNodeName, map[string]int64{"cpudevsocket99": 2})},
			expectedError: true,
		},
		{
			name:          "NUMAGrouped_DualSocketHT_DeviceNotFound_NUMA",
			cpuInfos:      mockCPUInfos_DualSocket_4CPUsPerSocket_HT,
			groupBy:       GROUP_BY_NUMA_NODE,
			claims:        []*resourceapi.ResourceClaim{testClaim(claimUID, testDriverName, testNodeName, map[string]int64{"cpudevnuma99": 2})},
			expectedError: true,
		},
		{
			name:           "SocketGrouped_DualSocketHT_MultiSocketRequest",
			cpuInfos:       mockCPUInfos_DualSocket_4CPUsPerSocket_HT,
			groupBy:        GROUP_BY_SOCKET,
			claims:         []*resourceapi.ResourceClaim{testClaim(claimUID, testDriverName, testNodeName, map[string]int64{"cpudevsocket0": 2, "cpudevsocket1": 2})},
			expectedCPUSet: cpuset.New(0, 2, 4, 6),
		},
		{
			name:           "NUMAGrouped_DualSocketHT_MultiNumaRequest",
			cpuInfos:       mockCPUInfos_DualSocket_4CPUsPerSocket_HT,
			groupBy:        GROUP_BY_NUMA_NODE,
			claims:         []*resourceapi.ResourceClaim{testClaim(claimUID, testDriverName, testNodeName, map[string]int64{"cpudevnuma0": 2, "cpudevnuma1": 2})},
			expectedCPUSet: cpuset.New(0, 2, 4, 6),
		},
		{
			name:               "SocketGrouped_SingleSocketHT_FullAlloc_Socket",
			cpuInfos:           mockCPUInfos_SingleSocket_4CPUS_HT,
			groupBy:            GROUP_BY_SOCKET,
			initialAllocations: map[types.UID]cpuset.CPUSet{"other-claim": cpuset.New(0, 2)},
			claims:             []*resourceapi.ResourceClaim{testClaim(claimUID, testDriverName, testNodeName, map[string]int64{"cpudevsocket0": 2})},
			expectedCPUSet:     cpuset.New(1, 3),
		},
		{
			name:           "SocketGrouped_SingleSocketHT_AllocAllCPU",
			cpuInfos:       mockCPUInfos_SingleSocket_4CPUS_HT,
			groupBy:        GROUP_BY_SOCKET,
			claims:         []*resourceapi.ResourceClaim{testClaim(claimUID, testDriverName, testNodeName, map[string]int64{"cpudevsocket0": 4})},
			expectedCPUSet: cpuset.New(0, 1, 2, 3),
		},
		{
			name:           "NUMAGrouped_DualSocketHT_AllocAllCPU_NUMA0",
			cpuInfos:       mockCPUInfos_DualSocket_4CPUsPerSocket_HT,
			groupBy:        GROUP_BY_NUMA_NODE,
			claims:         []*resourceapi.ResourceClaim{testClaim(claimUID, testDriverName, testNodeName, map[string]int64{"cpudevnuma0": 4})},
			expectedCPUSet: cpuset.New(0, 1, 4, 5),
		},
		{
			name:          "SocketGrouped_DualSocketHT_MoreThanAvailable",
			cpuInfos:      mockCPUInfos_DualSocket_4CPUsPerSocket_HT,
			groupBy:       GROUP_BY_SOCKET,
			claims:        []*resourceapi.ResourceClaim{testClaim(claimUID, testDriverName, testNodeName, map[string]int64{"cpudevsocket0": 5})},
			expectedError: true,
		},
		{
			name:            "SocketGrouped_DualSocketHT_CDIError",
			cpuInfos:        mockCPUInfos_DualSocket_4CPUsPerSocket_HT,
			groupBy:         GROUP_BY_SOCKET,
			claims:          []*resourceapi.ResourceClaim{testClaim(claimUID, testDriverName, testNodeName, map[string]int64{"cpudevsocket0": 2})},
			mockCdiAddError: fmt.Errorf("cdi error"),
			expectedError:   true,
		},
		{
			name:           "SocketGrouped_TopoSingleSocketHT_Off_Alloc2CPU",
			cpuInfos:       mockCPUInfos_SingleSocket_4CPUs_HT_Off,
			groupBy:        GROUP_BY_SOCKET,
			claims:         []*resourceapi.ResourceClaim{testClaim(claimUID, testDriverName, testNodeName, map[string]int64{"cpudevsocket0": 2})},
			expectedCPUSet: cpuset.New(0, 1),
		},
		{
			name:           "NUMAGrouped_TopoSingleSocketHT_Off_Alloc2CPU",
			cpuInfos:       mockCPUInfos_SingleSocket_4CPUs_HT_Off,
			groupBy:        GROUP_BY_NUMA_NODE,
			claims:         []*resourceapi.ResourceClaim{testClaim(claimUID, testDriverName, testNodeName, map[string]int64{"cpudevnuma0": 2})},
			expectedCPUSet: cpuset.New(0, 1),
		},
		{
			name:           "SocketGrouped_TopoSingleSocketHT_Off_AllocAllCPU",
			cpuInfos:       mockCPUInfos_SingleSocket_4CPUs_HT_Off,
			groupBy:        GROUP_BY_SOCKET,
			claims:         []*resourceapi.ResourceClaim{testClaim(claimUID, testDriverName, testNodeName, map[string]int64{"cpudevsocket0": 4})},
			expectedCPUSet: cpuset.New(0, 1, 2, 3),
		},
		{
			name:          "SocketGrouped_TopoSingleSocketHT_Off_MoreThanAvailable",
			cpuInfos:      mockCPUInfos_SingleSocket_4CPUs_HT_Off,
			groupBy:       GROUP_BY_SOCKET,
			claims:        []*resourceapi.ResourceClaim{testClaim(claimUID, testDriverName, testNodeName, map[string]int64{"cpudevsocket0": 5})},
			expectedError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			driver := baseCPUDriver(tc.groupBy, tc.cpuInfos, tc.initialAllocations, tc.reservedCPUs)
			mockCdiMgr := newMockCdiMgr()
			mockCdiMgr.addError = tc.mockCdiAddError
			driver.cdiMgr = mockCdiMgr

			preparedClaims, err := driver.PrepareResourceClaims(context.Background(), tc.claims)
			require.NoError(t, err)

			if len(tc.claims) > 0 {
				claimUID := tc.claims[0].UID
				result := preparedClaims[claimUID]
				if tc.expectedError {
					require.Error(t, result.Err, "Expected error for test case: %s", tc.name)
					require.Empty(t, result.Devices)
				} else {
					require.NoError(t, result.Err, "Unexpected error for test case: %s", tc.name)

					// Build expected devices based on the claim request
					expectedPreparedDevices := []kubeletplugin.Device{}
					if tc.expectedCPUSet.Size() != 0 || tc.expectedError {
						for _, res := range tc.claims[0].Status.Allocation.Devices.Results {
							expectedPreparedDevices = append(expectedPreparedDevices, kubeletplugin.Device{
								PoolName:     res.Pool,
								DeviceName:   res.Device,
								CDIDeviceIDs: []string{cdiQualifiedName},
								Requests:     []string{res.Request},
							})
						}
					}
					require.ElementsMatch(t, expectedPreparedDevices, result.Devices)

					envVar := mockCdiMgr.devices[cdiDeviceName]
					parts := strings.SplitN(envVar, "=", 2)
					// if expectedCPUSet is empty, parts[1] can be empty
					if tc.expectedCPUSet.Size() > 0 {
						require.Len(t, parts, 2, "CDI env var format error")
					} else {
						require.True(t, len(parts) == 2 || len(parts) == 1, "CDI env var format error")
					}

					actualCPUSet := cpuset.New()
					if len(parts) == 2 && parts[1] != "" {
						var err error
						actualCPUSet, err = cpuset.Parse(parts[1])
						require.NoError(t, err, "Failed to parse actual CPUSet from env var")
					}
					require.True(t, actualCPUSet.Equals(tc.expectedCPUSet), "Expected CPUSet %s, but got %s for test case %s", tc.expectedCPUSet.String(), actualCPUSet.String(), tc.name)
					if tc.expectedCPUSet.Size() > 0 {
						require.Equal(t, 1, len(mockCdiMgr.devices), "Expected 1 CDI device to be created")
					} else {
						require.Equal(t, 0, len(mockCdiMgr.devices), "Expected 0 CDI devices to be created")
					}
				}
			} else {
				require.Len(t, preparedClaims, 0)
				require.Len(t, mockCdiMgr.devices, 0)
			}
		})
	}
}

func TestUnprepareResourceClaims(t *testing.T) {
	claimUID := types.UID("test-claim-uid")

	testCases := []struct {
		name               string
		claims             []kubeletplugin.NamespacedObject
		cdiRemoveError     error
		expectUnprepareErr bool
		expectedResults    int
	}{
		{
			name:            "success",
			claims:          []kubeletplugin.NamespacedObject{{UID: claimUID}},
			expectedResults: 1,
		},
		{
			name:            "no claims",
			claims:          []kubeletplugin.NamespacedObject{},
			expectedResults: 0,
		},
		{
			name:               "unprepare error - cdi remove fails",
			claims:             []kubeletplugin.NamespacedObject{{UID: claimUID}},
			cdiRemoveError:     fmt.Errorf("cdi remove error"),
			expectUnprepareErr: true,
			expectedResults:    1,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockCdiMgr := newMockCdiMgr()
			mockCdiMgr.removeError = tc.cdiRemoveError
			mockProvider := &cpuinfo.MockCPUInfoProvider{CPUInfos: mockCPUInfos_SingleSocket_4CPUS_HT}
			topo, _ := mockProvider.GetCPUTopology()
			cp := &CPUDriver{
				cdiMgr:             mockCdiMgr,
				cpuAllocationStore: store.NewCPUAllocation(topo, cpuset.New()),
			}

			unpreparedClaims, err := cp.UnprepareResourceClaims(context.Background(), tc.claims)
			require.NoError(t, err)
			require.Len(t, unpreparedClaims, tc.expectedResults)

			if tc.expectedResults > 0 {
				if tc.expectUnprepareErr {
					require.Error(t, unpreparedClaims[claimUID])
				} else {
					require.NoError(t, unpreparedClaims[claimUID])
				}
			}
		})
	}
}

func testClaim(claimUID types.UID, driverName, poolName string, consumedCapacity map[string]int64) *resourceapi.ResourceClaim {
	results := []resourceapi.DeviceRequestAllocationResult{}
	for device, quantity := range consumedCapacity {
		results = append(results, resourceapi.DeviceRequestAllocationResult{
			Driver:           driverName,
			Pool:             poolName,
			Device:           device,
			ConsumedCapacity: map[resourceapi.QualifiedName]resource.Quantity{cpuResourceQualifiedName: *resource.NewQuantity(quantity, resource.DecimalSI)},
		})
	}
	return &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{UID: claimUID, Name: string(claimUID)},
		Status: resourceapi.ResourceClaimStatus{
			Allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{
					Results: results,
				},
			},
		},
	}
}
