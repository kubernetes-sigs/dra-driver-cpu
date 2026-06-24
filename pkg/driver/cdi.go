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

	"github.com/go-logr/logr"
	cdiapi "tags.cncf.io/container-device-interface/pkg/cdi"
	cdiSpec "tags.cncf.io/container-device-interface/specs-go"
)

const (
	cdiSpecVersion  = "0.8.0"
	cdiVendor       = "dra.k8s.io"
	cdiClass        = "cpu"
	cdiEnvVarPrefix = "DRA_CPUSET"
	// This mirrors the semantics of the assigned.cpuset introduced in k/k KEP #6122.
	cdiEnvVarAssigned = "DRA_CPUSET_ASSIGNED"
	cdiSpecDir        = "/var/run/cdi"
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
func (c *CdiManager) AddDevice(logger logr.Logger, deviceName string, envVars []string) error {
	specName := c.getSpecName(deviceName)

	spec := &cdiSpec.Spec{
		Version: cdiSpecVersion,
		Kind:    c.cdiKind,
		Devices: []cdiSpec.Device{
			{
				Name: deviceName,
				ContainerEdits: cdiSpec.ContainerEdits{
					Env: envVars,
				},
			},
		},
	}

	if err := c.cache.WriteSpec(spec, specName); err != nil {
		return fmt.Errorf("failed to write CDI spec %q: %w", specName, err)
	}

	logger.V(4).Info("Added CDI device", "deviceName", deviceName, "specName", specName, "envVars", envVars)
	return nil
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
