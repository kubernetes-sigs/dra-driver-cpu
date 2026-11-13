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
	"maps"
	"slices"

	"github.com/go-logr/logr"
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdiparser "tags.cncf.io/container-device-interface/pkg/parser"
	cdiSpec "tags.cncf.io/container-device-interface/specs-go"
)

const (
	cdiSpecVersion  = "0.8.0"
	cdiVendor       = "dra.k8s.io"
	cdiClass        = "cpu"
	cdiEnvVarPrefix = "DRA_CPUSET"
	cdiSpecDir      = "/var/run/cdi"
)

// CdiManager handles the lifecycle of CDI allocations for the driver.
type CdiManager struct {
	cache      *cdiapi.Cache
	cdiKind    string
	driverName string
}

// NewCdiManager creates a manager for the driver's CDI allocations.
func NewCdiManager(logger logr.Logger, driverName string, cdiDir string) (*CdiManager, error) {
	cache, err := cdiapi.NewCache(
		cdiapi.WithSpecDirs(cdiDir),
		// Disabled because we manage state entirely via the filesystem
		// and write individual stateless files per device.
		cdiapi.WithAutoRefresh(false),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to create CDI cache: %w", err)
	}

	c := &CdiManager{
		cache:      cache,
		cdiKind:    fmt.Sprintf("%s/%s", cdiVendor, cdiClass),
		driverName: driverName,
	}

	logger.Info("Initialized CDI manager", "driverName", driverName, "cdiDir", cdiDir)
	return c, nil
}

// getSpecName generates a unique, sanitized filename for a specific device allocation.
func (c *CdiManager) getSpecName(deviceName string) string {
	return cdiapi.GenerateTransientSpecName(cdiVendor, cdiClass, deviceName) + ".json"
}

// AddDevice writes a dedicated CDI spec file for a single device allocation.
func (c *CdiManager) AddDevice(logger logr.Logger, deviceName string, envVar string) error {
	return c.AddRequestDevices(logger, deviceName, map[string]string{deviceName: envVar})
}

func (c *CdiManager) AddRequestDevices(logger logr.Logger, specDeviceName string, envs map[string]string) error {
	specName := c.getSpecName(specDeviceName)
	devices := make([]cdiSpec.Device, 0, len(envs))
	for _, name := range slices.Sorted(maps.Keys(envs)) {
		devices = append(devices, cdiSpec.Device{Name: name, ContainerEdits: cdiSpec.ContainerEdits{Env: []string{envs[name]}}})
	}
	spec := &cdiSpec.Spec{Version: cdiSpecVersion, Kind: c.cdiKind, Devices: devices}
	if err := c.cache.WriteSpec(spec, specName); err != nil {
		return fmt.Errorf("failed to write CDI spec %q; check CDI directory permissions: %w", specName, err)
	}
	logger.V(4).Info("added CDI devices", "specName", specName, "count", len(devices))
	return nil
}

// Refresh reloads the CDI specs managed by the cache.
func (c *CdiManager) Refresh() error {
	if err := c.cache.Refresh(); err != nil {
		return fmt.Errorf("failed to refresh CDI cache: %w", err)
	}
	return nil
}

// GetDeviceEnv returns the environment edits for a specific CDI device allocation
// from the current cache. Call Refresh before lookup to load the latest on-disk specs.
func (c *CdiManager) GetDeviceEnv(deviceName string) ([]string, error) {
	device := c.cache.GetDevice(cdiparser.QualifiedName(cdiVendor, cdiClass, deviceName))
	if device == nil {
		return nil, fmt.Errorf("failed to find CDI device %q", deviceName)
	}
	return append([]string{}, device.ContainerEdits.Env...), nil
}

// RemoveDevice deletes the dedicated CDI spec file for a single device allocation.
func (c *CdiManager) RemoveDevice(logger logr.Logger, deviceName string) error {
	specName := c.getSpecName(deviceName)

	if err := c.cache.RemoveSpec(specName); err != nil {
		return fmt.Errorf("failed to remove CDI spec %q: %w", specName, err)
	}

	logger.V(4).Info("Removed CDI device", "deviceName", deviceName, "specName", specName)
	return nil
}

func (c *CdiManager) GetRequestPeers(deviceName string) ([]string, error) {
	device := c.cache.GetDevice(cdiparser.QualifiedName(cdiVendor, cdiClass, deviceName))
	if device == nil {
		return nil, fmt.Errorf("CDI request device %q is missing; restore its claim spec", deviceName)
	}
	var envs []string
	for _, peer := range device.GetSpec().Devices {
		envs = append(envs, peer.ContainerEdits.Env...)
	}
	return envs, nil
}
