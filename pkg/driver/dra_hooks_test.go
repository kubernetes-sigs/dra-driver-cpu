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
	"testing"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceslice"
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

type mockCPUInfoProvider struct {
	cpuInfos []cpuinfo.CPUInfo
	err      error
}

func (m *mockCPUInfoProvider) GetCPUInfos() ([]cpuinfo.CPUInfo, error) {
	return m.cpuInfos, m.err
}

var (
	// Sibling CPUs are non-consecutive: (0,2), (1,3)
	mockCPUInfos_SingleSocket_4CPUS_HT = []cpuinfo.CPUInfo{
		{CpuID: 0, CoreID: 0, SocketID: 0, NumaNode: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 2},
		{CpuID: 1, CoreID: 1, SocketID: 0, NumaNode: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 3},
		{CpuID: 2, CoreID: 0, SocketID: 0, NumaNode: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 0},
		{CpuID: 3, CoreID: 1, SocketID: 0, NumaNode: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 1},
	}
	mockCPUInfos_SingleSocket_4CPUs_HT_Off = []cpuinfo.CPUInfo{
		{CpuID: 0, CoreID: 0, SocketID: 0, NumaNode: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: -1},
		{CpuID: 1, CoreID: 1, SocketID: 0, NumaNode: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: -1},
		{CpuID: 2, CoreID: 2, SocketID: 0, NumaNode: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: -1},
		{CpuID: 3, CoreID: 3, SocketID: 0, NumaNode: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: -1},
	}
	// P-core sibling is non-consecutive: (0,2)
	mockCPUInfos_SingleSocket_Hybrid_HT = []cpuinfo.CPUInfo{
		{CpuID: 0, CoreID: 0, SocketID: 0, NumaNode: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 2},
		{CpuID: 1, CoreID: 1, SocketID: 0, NumaNode: 0, CoreType: cpuinfo.CoreTypeEfficiency, SiblingCpuID: 3},
		{CpuID: 2, CoreID: 0, SocketID: 0, NumaNode: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 0},
		{CpuID: 3, CoreID: 1, SocketID: 0, NumaNode: 0, CoreType: cpuinfo.CoreTypeEfficiency, SiblingCpuID: 1},
	}
	// 2 sockets, 2 cores/socket, HT on. Total 8 logical CPUs.
	mockCPUInfos_DualSocket_4CPUsPerSocket_HT = []cpuinfo.CPUInfo{
		{CpuID: 0, CoreID: 0, SocketID: 0, NumaNode: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 4},
		{CpuID: 1, CoreID: 1, SocketID: 0, NumaNode: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 5},
		{CpuID: 2, CoreID: 2, SocketID: 1, NumaNode: 1, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 6},
		{CpuID: 3, CoreID: 3, SocketID: 1, NumaNode: 1, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 7},
		{CpuID: 4, CoreID: 0, SocketID: 0, NumaNode: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 0},
		{CpuID: 5, CoreID: 1, SocketID: 0, NumaNode: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 1},
		{CpuID: 6, CoreID: 2, SocketID: 1, NumaNode: 1, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 2},
		{CpuID: 7, CoreID: 3, SocketID: 1, NumaNode: 1, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: 3},
	}
	mockCPUInfos_ExceedsSliceLimit = func() []cpuinfo.CPUInfo {
		var infos []cpuinfo.CPUInfo
		for i := 0; i < maxDevicesPerResourceSlice+1; i++ {
			infos = append(infos, cpuinfo.CPUInfo{CpuID: i, CoreID: i, SocketID: 0, NumaNode: 0, CoreType: cpuinfo.CoreTypePerformance, SiblingCpuID: -1})
		}
		return infos
	}()
)

func TestPublishResources(t *testing.T) {
	testCases := []struct {
		name              string
		cpuInfos          []cpuinfo.CPUInfo
		cpuInfoErr        error
		publishError      error
		expectPublish     bool
		expectedNumSlices int
		expectedDevices   int
	}{
		{
			name:              "single socket, HT on",
			cpuInfos:          mockCPUInfos_SingleSocket_4CPUS_HT,
			expectPublish:     true,
			expectedNumSlices: 1,
			expectedDevices:   len(mockCPUInfos_SingleSocket_4CPUS_HT),
		},
		{
			name:              "single socket, HT off",
			cpuInfos:          mockCPUInfos_SingleSocket_4CPUs_HT_Off,
			expectPublish:     true,
			expectedNumSlices: 1,
			expectedDevices:   len(mockCPUInfos_SingleSocket_4CPUs_HT_Off),
		},
		{
			name:              "single socket, hybrid",
			cpuInfos:          mockCPUInfos_SingleSocket_Hybrid_HT,
			expectPublish:     true,
			expectedNumSlices: 1,
			expectedDevices:   len(mockCPUInfos_SingleSocket_Hybrid_HT),
		},
		{
			name:              "dual socket, HT on, realistic numbering",
			cpuInfos:          mockCPUInfos_DualSocket_4CPUsPerSocket_HT,
			expectPublish:     true,
			expectedNumSlices: 2, // One slice per NUMA node
			expectedDevices:   len(mockCPUInfos_DualSocket_4CPUsPerSocket_HT),
		},
		{
			name:          "no devices to publish",
			cpuInfos:      []cpuinfo.CPUInfo{},
			expectPublish: false,
		},
		{
			name:          "error getting cpu info",
			cpuInfoErr:    fmt.Errorf("cpuinfo error"),
			expectPublish: false,
		},
		{
			name:              "error publishing",
			cpuInfos:          mockCPUInfos_SingleSocket_4CPUS_HT,
			publishError:      fmt.Errorf("publish error"),
			expectPublish:     true,
			expectedNumSlices: 1,
			expectedDevices:   len(mockCPUInfos_SingleSocket_4CPUS_HT),
		},
		{
			name:              "error because devices on one NUMA > maxDevicesPerResourceSlice",
			cpuInfos:          mockCPUInfos_ExceedsSliceLimit,
			expectPublish:     false,
			expectedNumSlices: 0,
			expectedDevices:   0,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mockPlugin := &mockKubeletPlugin{publishError: tc.publishError}
			mockProvider := &mockCPUInfoProvider{cpuInfos: tc.cpuInfos, err: tc.cpuInfoErr}
			cp := &CPUDriver{
				nodeName:          testNodeName,
				draPlugin:         mockPlugin,
				cpuIDToDeviceName: make(map[int]string),
				deviceNameToCPUID: make(map[string]int),
				cpuInfoProvider:   mockProvider,
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
			require.Equal(t, tc.expectedDevices, len(cp.cpuIDToDeviceName))
			require.Equal(t, tc.expectedDevices, len(cp.deviceNameToCPUID))

			// Verify device attributes
			for _, slice := range pool.Slices {
				for _, device := range slice.Devices {
					cpuID, ok := cp.deviceNameToCPUID[device.Name]
					require.True(t, ok)

					var cpuInfo *cpuinfo.CPUInfo
					for i := range tc.cpuInfos {
						if tc.cpuInfos[i].CpuID == cpuID {
							cpuInfo = &tc.cpuInfos[i]
							break
						}
					}
					require.NotNil(t, cpuInfo, "could not find matching cpuInfo for device %s", device.Name)

					numaNode := int64(cpuInfo.NumaNode)
					l3CacheID := int64(cpuInfo.L3CacheID)
					coreType := cpuInfo.CoreType.String()
					socketID := int64(cpuInfo.SocketID)

					require.Equal(t, &numaNode, device.Attributes["dra.cpu/numaNode"].IntValue)
					require.Equal(t, &l3CacheID, device.Attributes["dra.cpu/l3CacheID"].IntValue)
					require.Equal(t, &coreType, device.Attributes["dra.cpu/coreType"].StringValue)
					require.Equal(t, &socketID, device.Attributes["dra.cpu/socketID"].IntValue)
				}
			}
		})
	}
}

func TestPrepareResourceClaims(t *testing.T) {
	mockProvider := &mockCPUInfoProvider{cpuInfos: mockCPUInfos_SingleSocket_4CPUS_HT}
	baseCPUDriver := func() *CPUDriver {
		return &CPUDriver{
			driverName: testDriverName,
			deviceNameToCPUID: map[string]int{
				"cpudev0": 0,
				"cpudev1": 1,
			},
			cpuAllocationStore: NewCPUAllocationStore(mockProvider),
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
			mockProvider := &mockCPUInfoProvider{cpuInfos: mockCPUInfos_SingleSocket_4CPUS_HT}
			cp := &CPUDriver{
				cdiMgr:             mockCdiMgr,
				cpuAllocationStore: NewCPUAllocationStore(mockProvider),
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
