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
	"path/filepath"
	"slices"

	"github.com/go-logr/logr"
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
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

// getSpecName generates a unique, sanitized filename for a claim's CDI spec.
func (c *CdiManager) getSpecName(specKey string) string {
	return cdiapi.GenerateTransientSpecName(cdiVendor, cdiClass, specKey) + ".json"
}

// AddDevice writes a single CDI spec file for specKey containing one device per
// entry in deviceEnvs (deviceName -> envVar).
func (c *CdiManager) AddDevice(logger logr.Logger, specKey string, deviceEnvs map[string]string) error {
	specName := c.getSpecName(specKey)

	devices := make([]cdiSpec.Device, 0, len(deviceEnvs))
	for _, deviceName := range slices.Sorted(maps.Keys(deviceEnvs)) {
		devices = append(devices, cdiSpec.Device{
			Name: deviceName,
			ContainerEdits: cdiSpec.ContainerEdits{
				Env: []string{deviceEnvs[deviceName]},
			},
		})
	}

	spec := &cdiSpec.Spec{
		Version: cdiSpecVersion,
		Kind:    c.cdiKind,
		Devices: devices,
	}

	if err := c.cache.WriteSpec(spec, specName); err != nil {
		return fmt.Errorf("failed to write CDI spec %q: %w", specName, err)
	}

	logger.V(4).Info("Added CDI devices", "specKey", specKey, "specName", specName, "devices", deviceEnvs)
	return nil
}

// Refresh reloads the CDI specs managed by the cache.
func (c *CdiManager) Refresh() error {
	if err := c.cache.Refresh(); err != nil {
		return fmt.Errorf("failed to refresh CDI cache: %w", err)
	}
	return nil
}

// GetDeviceEnv returns all environment edits from the CDI spec file for specKey
// in the current cache. Call Refresh before lookup to load the latest on-disk specs.
func (c *CdiManager) GetDeviceEnv(specKey string) ([]string, error) {
	targetSpecName := c.getSpecName(specKey)
	for _, spec := range c.cache.GetVendorSpecs(cdiVendor) {
		if spec.GetClass() == cdiClass && filepath.Base(spec.GetPath()) == targetSpecName {
			var envs []string
			for _, dev := range spec.Devices {
				envs = append(envs, dev.ContainerEdits.Env...)
			}
			return envs, nil
		}
	}
	return nil, fmt.Errorf("failed to find CDI spec %q", targetSpecName)
}

// RemoveDevice deletes the CDI spec file for specKey.
func (c *CdiManager) RemoveDevice(logger logr.Logger, specKey string) error {
	specName := c.getSpecName(specKey)

	if err := c.cache.RemoveSpec(specName); err != nil {
		return fmt.Errorf("failed to remove CDI spec %q: %w", specName, err)
	}

	logger.V(4).Info("Removed CDI spec", "specKey", specKey, "specName", specName)
	return nil
}
