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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"k8s.io/klog/v2"
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

// CdiManager manages a single CDI JSON spec file using a mutex for thread safety.
type CdiManager struct {
	path       string
	mutex      sync.Mutex
	cdiKind    string
	driverName string
}

// NewCdiManager creates a manager for the driver's CDI spec file.
func NewCdiManager(driverName string) (*CdiManager, error) {
	path := filepath.Join(cdiSpecDir, fmt.Sprintf("%s.json", driverName))

	if err := os.MkdirAll(cdiSpecDir, 0755); err != nil {
		return nil, fmt.Errorf("error creating CDI spec directory %q: %w", cdiSpecDir, err)
	}

	cdiKind := fmt.Sprintf("%s/%s", cdiVendor, cdiClass)
	c := &CdiManager{
		path:       path,
		cdiKind:    cdiKind,
		driverName: driverName,
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		initialSpec := &cdiSpec.Spec{
			Version: cdiSpecVersion,
			Kind:    cdiKind,
			Devices: []cdiSpec.Device{},
		}
		if err := c.writeSpecToFile(initialSpec); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, fmt.Errorf("error accessing CDI spec file %q: %w", path, err)
	}

	klog.Infof("Initialized CDI file manager for %q", path)
	return c, nil
}

// AddDevice adds a device to the CDI spec file.
func (c *CdiManager) AddDevice(deviceName string, envVar string) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	spec, err := c.readSpecFromFile()
	if err != nil {
		return err
	}

	// Remove any existing device with the same name to make this call idempotent.
	removeDeviceFromSpec(spec, deviceName)
	newDevice := cdiSpec.Device{
		Name: deviceName,
		ContainerEdits: cdiSpec.ContainerEdits{
			Env: []string{
				envVar,
			},
		},
	}

	spec.Devices = append(spec.Devices, newDevice)
	return c.writeSpecToFile(spec)
}

// RemoveDevice removes a device from the CDI spec file.
func (c *CdiManager) RemoveDevice(deviceName string) error {
	c.mutex.Lock()
	defer c.mutex.Unlock()

	spec, err := c.readSpecFromFile()
	if err != nil {
		if os.IsNotExist(err) {
			return nil // File already gone, nothing to do.
		}
		return err
	}

	if removeDeviceFromSpec(spec, deviceName) {
		return c.writeSpecToFile(spec)
	}

	return nil
}

func (c *CdiManager) GetSpec() (*cdiSpec.Spec, error) {
	return c.readSpecFromFile()
}

func removeDeviceFromSpec(spec *cdiSpec.Spec, deviceName string) bool {
	deviceFound := false
	newDevices := []cdiSpec.Device{}
	for _, d := range spec.Devices {
		if d.Name != deviceName {
			newDevices = append(newDevices, d)
		} else {
			deviceFound = true
		}
	}
	spec.Devices = newDevices
	return deviceFound
}

func (c *CdiManager) readSpecFromFile() (*cdiSpec.Spec, error) {
	data, err := os.ReadFile(c.path)
	if err != nil {
		return nil, fmt.Errorf("error reading CDI spec file %q: %w", c.path, err)
	}

	if len(data) == 0 {
		return &cdiSpec.Spec{
			Version: cdiSpecVersion,
			Kind:    c.cdiKind,
			Devices: []cdiSpec.Device{},
		}, nil
	}

	spec := &cdiSpec.Spec{}
	if err := json.Unmarshal(data, spec); err != nil {
		return nil, fmt.Errorf("error unmarshaling CDI spec from %q: %w", c.path, err)
	}
	klog.Infof("Read CDI spec from %q spec:%+v", c.path, spec)
	return spec, nil
}

func (c *CdiManager) writeSpecToFile(spec *cdiSpec.Spec) (err error) {
	tmpFile, err := os.CreateTemp(cdiSpecDir, c.driverName)
	if err != nil {
		return fmt.Errorf("failed to create temporary CDI spec: %w", err)
	}
	defer func() {
		// avoid file descriptor leakage or undeterministic closure
		// note we ignore the error; this is intentional because in the happy
		// path we will have a double close(), which is however harmless.
		_ = tmpFile.Close()
		if err != nil {
			_ = os.Remove(tmpFile.Name())
		}
	}()

	data, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshaling CDI spec: %w", err)
	}

	if _, err := tmpFile.Write(data); err != nil {
		return fmt.Errorf("failed to write to temporary CDI spec: %w", err)
	}

	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync temporary CDI spec: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temporary CDI spec: %w", err)
	}

	if err := os.Rename(tmpFile.Name(), c.path); err != nil {
		return fmt.Errorf("failed to rename temporary CDI spec: %w", err)
	}

	klog.Infof("Successfully updated and synced CDI spec file %q", c.path)
	return nil
}
