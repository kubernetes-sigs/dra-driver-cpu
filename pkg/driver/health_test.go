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

package driver

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/utils/cpuset"
)

func newHealthTestDriver(deviceNames ...string) *CPUDriver {
	cp := &CPUDriver{
		driverName: testDriverName,
		nodeName:   testNodeName,
		health:     newHealthTracker(),
	}
	for _, name := range deviceNames {
		cp.health.devices[name] = &deviceHealthEntry{status: kubeletplugin.HealthStatusHealthy, message: "device initialized"}
	}
	return cp
}

func TestSetDeviceHealth(t *testing.T) {
	logger := testr.New(t)

	t.Run("unknown device is ignored", func(t *testing.T) {
		cp := newHealthTestDriver("cpudev0")
		cp.setDeviceHealth(logger, "does-not-exist", kubeletplugin.HealthStatusUnhealthy, "boom")
		_, ok := cp.health.devices["does-not-exist"]
		assert.False(t, ok, "unknown device must not be added to the health map")
	})

	t.Run("known device is updated and change is reported", func(t *testing.T) {
		cp := newHealthTestDriver("cpudev0")
		clientCh := make(chan kubeletplugin.DeviceHealthReport, 1)
		cp.health.clientsMu.Lock()
		cp.health.clients = append(cp.health.clients, clientCh)
		cp.health.clientsMu.Unlock()

		cp.setDeviceHealth(logger, "cpudev0", kubeletplugin.HealthStatusUnhealthy, "cdi write failed")

		entry := cp.health.devices["cpudev0"]
		require.Equal(t, kubeletplugin.HealthStatusUnhealthy, entry.status)
		require.Equal(t, "cdi write failed", entry.message)

		select {
		case report := <-clientCh:
			require.Len(t, report.Devices, 1)
			assert.Equal(t, "cpudev0", report.Devices[0].DeviceName)
			assert.Equal(t, testNodeName, report.Devices[0].PoolName)
			assert.Equal(t, kubeletplugin.HealthStatusUnhealthy, report.Devices[0].Health)
			assert.Equal(t, "cdi write failed", report.Devices[0].Message)
		default:
			t.Fatal("expected a health report to be sent to the client channel")
		}
	})

	t.Run("no-op update does not notify clients", func(t *testing.T) {
		cp := newHealthTestDriver("cpudev0")
		clientCh := make(chan kubeletplugin.DeviceHealthReport, 1)
		cp.health.clientsMu.Lock()
		cp.health.clients = append(cp.health.clients, clientCh)
		cp.health.clientsMu.Unlock()

		// Same status and message as the initial state set by newHealthTestDriver.
		cp.setDeviceHealth(logger, "cpudev0", kubeletplugin.HealthStatusHealthy, "device initialized")

		select {
		case <-clientCh:
			t.Fatal("did not expect a health report for a no-op update")
		default:
		}
	})
}

func TestMarkCPUSetDevicesHealth(t *testing.T) {
	logger := testr.New(t)

	t.Run("ungrouped mode maps cpus to devices", func(t *testing.T) {
		cp := newHealthTestDriver("cpudev0", "cpudev1", "cpudev2")
		cp.topology.cpuIDToDeviceName = map[int]string{0: "cpudev0", 1: "cpudev1", 2: "cpudev2"}

		cp.markCPUSetDevicesHealth(logger, cpuset.New(0, 1), kubeletplugin.HealthStatusUnhealthy, "validation failed")

		assert.Equal(t, kubeletplugin.HealthStatusUnhealthy, cp.health.devices["cpudev0"].status)
		assert.Equal(t, kubeletplugin.HealthStatusUnhealthy, cp.health.devices["cpudev1"].status)
		assert.Equal(t, kubeletplugin.HealthStatusHealthy, cp.health.devices["cpudev2"].status, "cpu 2 was not part of the failing cpuset")
	})

	t.Run("grouped mode is a no-op", func(t *testing.T) {
		cp := newHealthTestDriver("cpudevsocket0")
		// cpuIDToDeviceName is intentionally left empty, as in grouped mode.

		cp.markCPUSetDevicesHealth(logger, cpuset.New(0, 1), kubeletplugin.HealthStatusUnhealthy, "validation failed")

		assert.Equal(t, kubeletplugin.HealthStatusHealthy, cp.health.devices["cpudevsocket0"].status)
	})

	t.Run("notifies clients once for the whole batch", func(t *testing.T) {
		cp := newHealthTestDriver("cpudev0", "cpudev1", "cpudev2")
		cp.topology.cpuIDToDeviceName = map[int]string{0: "cpudev0", 1: "cpudev1", 2: "cpudev2"}
		clientCh := make(chan kubeletplugin.DeviceHealthReport, healthClientBufferSize)
		cp.health.clientsMu.Lock()
		cp.health.clients = append(cp.health.clients, clientCh)
		cp.health.clientsMu.Unlock()

		// All three CPUs map to devices and change status, but subscribers
		// must only be notified once for the whole batch, not once per CPU.
		cp.markCPUSetDevicesHealth(logger, cpuset.New(0, 1, 2), kubeletplugin.HealthStatusUnhealthy, "validation failed")

		require.Len(t, clientCh, 1, "expected exactly one health report for the whole batch")
		report := <-clientCh
		require.Len(t, report.Devices, 3)
	})
}

func TestMarkClaimDevicesHealth(t *testing.T) {
	logger := testr.New(t)
	claimUID := types.UID("claim-1")

	t.Run("marks only this driver's devices", func(t *testing.T) {
		cp := newHealthTestDriver("cpudev0", "cpudev1")
		claim := testClaimWithResults(claimUID, []resourceapi.DeviceRequestAllocationResult{
			{Driver: testDriverName, Pool: testNodeName, Device: "cpudev0"},
			// Same device name this driver tracks, but attributed to a
			// different driver in the claim: must not be touched.
			{Driver: "other-driver.k8s.io", Pool: testNodeName, Device: "cpudev1"},
		})

		cp.markClaimDevicesHealth(logger, claim, kubeletplugin.HealthStatusUnhealthy, "cdi spec write failed")

		assert.Equal(t, kubeletplugin.HealthStatusUnhealthy, cp.health.devices["cpudev0"].status)
		assert.Equal(t, kubeletplugin.HealthStatusHealthy, cp.health.devices["cpudev1"].status)
	})

	t.Run("claim without allocation is a no-op", func(t *testing.T) {
		cp := newHealthTestDriver("cpudev0")
		claim := &resourceapi.ResourceClaim{}

		cp.markClaimDevicesHealth(logger, claim, kubeletplugin.HealthStatusUnhealthy, "should not apply")

		assert.Equal(t, kubeletplugin.HealthStatusHealthy, cp.health.devices["cpudev0"].status)
	})

	t.Run("notifies clients once for the whole batch", func(t *testing.T) {
		cp := newHealthTestDriver("cpudev0", "cpudev1")
		claim := testClaimWithResults(claimUID, []resourceapi.DeviceRequestAllocationResult{
			{Driver: testDriverName, Pool: testNodeName, Device: "cpudev0"},
			{Driver: testDriverName, Pool: testNodeName, Device: "cpudev1"},
		})
		clientCh := make(chan kubeletplugin.DeviceHealthReport, healthClientBufferSize)
		cp.health.clientsMu.Lock()
		cp.health.clients = append(cp.health.clients, clientCh)
		cp.health.clientsMu.Unlock()

		// Both devices belong to this driver and change status, but
		// subscribers must only be notified once for the whole claim, not
		// once per device.
		cp.markClaimDevicesHealth(logger, claim, kubeletplugin.HealthStatusUnhealthy, "cdi spec write failed")

		require.Len(t, clientCh, 1, "expected exactly one health report for the whole batch")
		report := <-clientCh
		require.Len(t, report.Devices, 2)
	})
}

func TestBuildHealthReport(t *testing.T) {
	cp := newHealthTestDriver("cpudev0", "cpudev1")
	cp.setDeviceHealth(testr.New(t), "cpudev1", kubeletplugin.HealthStatusUnhealthy, "broken")

	report := cp.buildHealthReport()
	require.Len(t, report.Devices, 2)

	byName := map[string]kubeletplugin.DeviceHealth{}
	for _, d := range report.Devices {
		byName[d.DeviceName] = d
	}

	assert.Equal(t, kubeletplugin.HealthStatusHealthy, byName["cpudev0"].Health)
	assert.Equal(t, kubeletplugin.HealthStatusUnhealthy, byName["cpudev1"].Health)
	assert.Equal(t, "broken", byName["cpudev1"].Message)
	for _, d := range report.Devices {
		assert.Equal(t, testNodeName, d.PoolName)
	}
}

func TestWatchHealthStatus(t *testing.T) {
	cp := newHealthTestDriver("cpudev0")
	ctx, cancel := context.WithCancel(context.Background())

	var wg sync.WaitGroup
	wg.Add(1)
	reports := make(chan kubeletplugin.DeviceHealthReport)
	var watchErr error
	go func() {
		defer wg.Done()
		watchErr = cp.WatchHealthStatus(ctx, reports)
	}()

	// The first report sent must be a full snapshot of the initial state.
	select {
	case report := <-reports:
		require.Len(t, report.Devices, 1)
		assert.Equal(t, kubeletplugin.HealthStatusHealthy, report.Devices[0].Health)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the initial health report")
	}

	// A subsequent health change must be streamed too.
	go cp.setDeviceHealth(testr.New(t), "cpudev0", kubeletplugin.HealthStatusUnhealthy, "device fault")

	select {
	case report := <-reports:
		require.Len(t, report.Devices, 1)
		assert.Equal(t, kubeletplugin.HealthStatusUnhealthy, report.Devices[0].Health)
		assert.Equal(t, "device fault", report.Devices[0].Message)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the updated health report")
	}

	cp.health.clientsMu.RLock()
	numClients := len(cp.health.clients)
	cp.health.clientsMu.RUnlock()
	require.Equal(t, 1, numClients, "expected exactly one registered health client while WatchHealthStatus is running")

	cancel()
	wg.Wait()
	require.NoError(t, watchErr)

	cp.health.clientsMu.RLock()
	numClients = len(cp.health.clients)
	cp.health.clientsMu.RUnlock()
	assert.Equal(t, 0, numClients, "expected the health client to be unregistered after WatchHealthStatus returns")
}

func TestWatchHealthStatusStopsOnDriverStop(t *testing.T) {
	cp := newHealthTestDriver("cpudev0")
	reports := make(chan kubeletplugin.DeviceHealthReport)

	done := make(chan error, 1)
	go func() {
		done <- cp.WatchHealthStatus(context.Background(), reports)
	}()

	// Drain the initial report so WatchHealthStatus reaches its main loop.
	select {
	case <-reports:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the initial health report")
	}

	close(cp.health.stopCh)

	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("WatchHealthStatus did not return after stopHealthCh was closed")
	}
}

func TestHealthResendLoop(t *testing.T) {
	originalInterval := healthResendInterval
	healthResendInterval = 10 * time.Millisecond
	defer func() { healthResendInterval = originalInterval }()

	cp := newHealthTestDriver("cpudev0")
	clientCh := make(chan kubeletplugin.DeviceHealthReport, 1)
	cp.health.clientsMu.Lock()
	cp.health.clients = append(cp.health.clients, clientCh)
	cp.health.clientsMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cp.health.wg.Add(1)
	go cp.healthResendLoop(ctx)

	select {
	case report := <-clientCh:
		require.Len(t, report.Devices, 1)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a periodic health resend")
	}

	cancel()
	cp.health.wg.Wait()
}
