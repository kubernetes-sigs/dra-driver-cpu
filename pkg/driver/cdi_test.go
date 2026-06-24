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
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	cdiSpec "tags.cncf.io/container-device-interface/specs-go"
)

// getSpecFromCache is a test helper to verify WriteSpec succeeded.
// It forces a cache refresh and searches for the specific dynamically generated filename.
func getSpecFromCache(mgr *CdiManager, targetSpecName string) *cdiSpec.Spec {
	_ = mgr.cache.Refresh()
	specs := mgr.cache.GetVendorSpecs(cdiVendor)
	for _, spec := range specs {
		if spec.GetClass() == cdiClass && filepath.Base(spec.GetPath()) == targetSpecName {
			return spec.Spec
		}
	}
	return nil
}

func TestAddDevice(t *testing.T) {
	testcases := []struct {
		name          string
		deviceName    string
		envVar        string
		simulateErr   bool
		expectedError string
	}{
		{
			name:        "successfully writes a new device spec to disk",
			deviceName:  "claim-cpu-add-success",
			envVar:      "CPU=2,3",
			simulateErr: false,
		},
		{
			name:          "fails to writes pec to disk",
			deviceName:    "claim-cpu-add-error",
			envVar:        "CPU=2,3",
			simulateErr:   true,
			expectedError: "failed to write CDI spec",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			logger := testr.New(t)
			tempCDIDir := t.TempDir()

			if tc.simulateErr {
				tempFile := filepath.Join(tempCDIDir, "invalid-dir-file")
				err := os.WriteFile(tempFile, []byte(""), 0600)
				require.NoError(t, err)
				tempCDIDir = tempFile
			}

			mgr, err := NewCdiManager(logger, testDriverName, tempCDIDir)
			require.NoError(t, err)

			expectedSpecName := mgr.getSpecName(tc.deviceName)
			expectedFilePath := filepath.Join(tempCDIDir, expectedSpecName)

			err = mgr.AddDevice(logger, tc.deviceName, []string{tc.envVar})

			if tc.expectedError != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedError)
				return
			}

			require.NoError(t, err)

			_, err = os.Stat(expectedFilePath)
			require.NoError(t, err, "expected CDI spec file to be created on disk")

			expectedSpec := &cdiSpec.Spec{
				Version: cdiSpecVersion,
				Kind:    cdiVendor + "/" + cdiClass,
				Devices: []cdiSpec.Device{
					{
						Name: tc.deviceName,
						ContainerEdits: cdiSpec.ContainerEdits{
							Env: []string{tc.envVar},
						},
					},
				},
			}

			got := getSpecFromCache(mgr, expectedSpecName)
			if diff := cmp.Diff(expectedSpec, got); diff != "" {
				t.Errorf("unexpected spec diff: %v", diff)
			}
		})
	}
}

func TestRemoveDevice(t *testing.T) {
	testcases := []struct {
		name          string
		deviceName    string
		envVar        string
		simulateErr   bool
		expectedError string
	}{
		{
			name:        "successfully removes an existing device spec from disk",
			deviceName:  "claim-cpu-remove-success",
			envVar:      "CPU=4,5",
			simulateErr: false,
		},
		{
			name:          "fails to remove spec when directory is actually a file",
			deviceName:    "claim-cpu-remove-error",
			envVar:        "CPU=4,5",
			simulateErr:   true,
			expectedError: "failed to remove CDI spec",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			logger := testr.New(t)
			tempCDIDir := t.TempDir()

			if tc.simulateErr {
				tempFile := filepath.Join(tempCDIDir, "invalid-dir-file")
				err := os.WriteFile(tempFile, []byte(""), 0600)
				require.NoError(t, err)
				tempCDIDir = tempFile
			}

			mgr, err := NewCdiManager(logger, testDriverName, tempCDIDir)
			require.NoError(t, err)

			expectedSpecName := mgr.getSpecName(tc.deviceName)
			expectedFilePath := filepath.Join(tempCDIDir, expectedSpecName)

			if !tc.simulateErr {
				err = mgr.AddDevice(logger, tc.deviceName, []string{tc.envVar})
				require.NoError(t, err)
			}

			err = mgr.RemoveDevice(logger, tc.deviceName)

			if tc.expectedError != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tc.expectedError)
				return
			}

			require.NoError(t, err)

			_, err = os.Stat(expectedFilePath)
			require.Error(t, err, "expected an error when stating a deleted file")
			require.True(t, os.IsNotExist(err), "expected file to not exist on disk, but got: %v", err)

			gotAfterRemove := getSpecFromCache(mgr, expectedSpecName)
			require.Nil(t, gotAfterRemove, "expected spec to be nil in cache after removal")
		})
	}
}

func TestAddDeviceOverwrite(t *testing.T) {
	logger := testr.New(t)
	tempCDIDir := t.TempDir()

	mgr, err := NewCdiManager(logger, testDriverName, tempCDIDir)
	require.NoError(t, err)

	deviceName := "claim-cpu-overwrite"
	expectedSpecName := mgr.getSpecName(deviceName)

	assertFileCount := func(expected int) {
		files, err := os.ReadDir(tempCDIDir)
		require.NoError(t, err)
		require.Len(t, files, expected)
	}

	err = mgr.AddDevice(logger, deviceName, []string{"CPU=0,1"})
	require.NoError(t, err)
	assertFileCount(1)

	// Verify the cache has the initial spec
	spec1 := getSpecFromCache(mgr, expectedSpecName)
	require.NotNil(t, spec1)
	require.Equal(t, []string{"CPU=0,1"}, spec1.Devices[0].ContainerEdits.Env)

	// Call AddDevice again with the same deviceName and same data
	err = mgr.AddDevice(logger, deviceName, []string{"CPU=0,1"})
	require.NoError(t, err)
	// Verify that we do not create a new file
	assertFileCount(1)
}
