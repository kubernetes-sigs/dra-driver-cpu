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
	"fmt"
	"path/filepath"
	"sort"
	"sync"

	"github.com/go-logr/logr"
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdiSpec "tags.cncf.io/container-device-interface/specs-go"
)

const (
	cdiSpecVersion  = "0.8.0"
	cdiVendor       = "dra.k8s.io"
	cdiClass        = "cpu"
	cdiEnvVarPrefix = "DRA_CPUSET"
)

var (
	cdiSpecDir = "/var/run/cdi"
)

// CdiManager handles the lifecycle of CDI allocations for the driver.
type CdiManager struct {
	mu            sync.Mutex
	cache         *cdiapi.Cache
	cdiKind       string
	driverName    string
	specName      string
	activeDevices map[string]cdiSpec.Device
}

// NewCdiManager creates a manager for the driver's CDI spec file.
func NewCdiManager(logger logr.Logger, driverName string) (*CdiManager, error) {
	cache, err := cdiapi.NewCache(
		cdiapi.WithSpecDirs(cdiSpecDir),
		cdiapi.WithAutoRefresh(false), // Disabled because we manually push our internal state to disk via syncToCache().
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create CDI cache: %w", err)
	}

	c := &CdiManager{
		cache:         cache,
		cdiKind:       fmt.Sprintf("%s/%s", cdiVendor, cdiClass),
		driverName:    driverName,
		specName:      fmt.Sprintf("%s.json", driverName),
		activeDevices: make(map[string]cdiSpec.Device),
	}

	// Recovery logic in case of restart.
	// We log errors but always attempt recovery because another driver's
	// malformed CDI file should not prevent us from recovering our own valid state.
	if err := c.cache.Refresh(); err != nil {
		logger.Info("Encountered errors refreshing CDI cache (attempting recovery anyway)", "error", err)
	}

	// Always check the cache, even if Refresh() returned errors.
	specs := c.cache.GetVendorSpecs(cdiVendor)

	for _, spec := range specs {
		if spec.GetClass() == cdiClass && filepath.Base(spec.GetPath()) == c.specName {
			for _, device := range spec.Devices {
				c.activeDevices[device.Name] = device
			}

			logger.Info("Successfully recovered state", "devices", len(c.activeDevices))
			break
		}
	}

	logger.Info("Initialized CDI manager with cache", "specName", c.specName)
	return c, nil
}

// AddDevice adds a device to the internal state and syncs to the CDI spec file.
func (c *CdiManager) AddDevice(logger logr.Logger, deviceName string, envVar string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Save previous state in case this is an overwrite (rare, but good for safety)
	oldDevice, existed := c.activeDevices[deviceName]

	// Update internal state
	c.activeDevices[deviceName] = cdiSpec.Device{
		Name: deviceName,
		ContainerEdits: cdiSpec.ContainerEdits{
			Env: []string{envVar},
		},
	}

	// Push the updated state to the cache
	if err := c.syncToCache(); err != nil {
		// Rollback memory state on failure to stay consistent with disk
		if existed {
			c.activeDevices[deviceName] = oldDevice
		} else {
			delete(c.activeDevices, deviceName)
		}
		return fmt.Errorf("failed to sync cache during addition (state rolled back): %w", err)
	}

	logger.V(4).Info("Added CDI device", "deviceName", deviceName, "env", envVar)
	return nil
}

// RemoveDevice removes a device from the internal state and syncs to the CDI spec file.
func (c *CdiManager) RemoveDevice(logger logr.Logger, deviceName string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	device, exists := c.activeDevices[deviceName]
	if !exists {
		// Device not found, nothing to do
		return nil
	}

	// Remove from internal state temporarily
	delete(c.activeDevices, deviceName)

	// Push the updated state to the cache
	if err := c.syncToCache(); err != nil {
		// Rollback memory state on failure so subsequent Kubelet retries
		// will actually re-attempt the disk write
		c.activeDevices[deviceName] = device
		return fmt.Errorf("failed to sync cache during removal (state rolled back): %w", err)
	}

	logger.V(4).Info("Removed CDI device", "deviceName", deviceName)
	return nil
}

// syncToCache generates a fresh CDI spec from the activeDevices map and writes it.
func (c *CdiManager) syncToCache() error {
	// If no devices are active, clean up the file entirely
	if len(c.activeDevices) == 0 {
		if err := c.cache.RemoveSpec(c.specName); err != nil {
			return fmt.Errorf("failed to remove empty CDI spec: %w", err)
		}
		return nil
	}

	// Build a brand new spec from scratch
	spec := &cdiSpec.Spec{
		Version: cdiSpecVersion,
		Kind:    c.cdiKind,
		Devices: make([]cdiSpec.Device, 0, len(c.activeDevices)),
	}

	// Populate the new spec's devices
	for _, device := range c.activeDevices {
		spec.Devices = append(spec.Devices, device)
	}

	// Sort devices by name to ensure deterministic order
	sort.Slice(spec.Devices, func(i, j int) bool {
		return spec.Devices[i].Name < spec.Devices[j].Name
	})

	if err := c.cache.WriteSpec(spec, c.specName); err != nil {
		return fmt.Errorf("failed to write CDI spec: %w", err)
	}

	return nil
}
