/*
 * Copyright 2025 The Kubernetes Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package driver

import (
	"path/filepath"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	cdiSpec "tags.cncf.io/container-device-interface/specs-go"
)

type testdevice struct {
	name string
	env  string
}

// getSpecFromCache is test helper to verify syncToCache succeeded.
// It forces a cache refresh and searches for our specific file, proving the disk write worked.
func getSpecFromCache(mgr *CdiManager) *cdiSpec.Spec {
	_ = mgr.cache.Refresh()
	specs := mgr.cache.GetVendorSpecs(cdiVendor)
	for _, spec := range specs {
		if spec.GetClass() == cdiClass && filepath.Base(spec.GetPath()) == mgr.specName {
			return spec.Spec
		}
	}
	return nil
}

func TestAddDevice(t *testing.T) {
	type testcase struct {
		name         string
		devices      []testdevice
		expectedSpec *cdiSpec.Spec
	}

	testcases := []testcase{
		{
			name:         "empty",
			expectedSpec: nil, // Because syncToCache removes the file when empty, the cache will return nil.
		},
		{
			name: "simple device",
			devices: []testdevice{
				{
					name: "foodev",
					env:  "FOO=42",
				},
			},
			expectedSpec: &cdiSpec.Spec{
				Version: cdiSpecVersion,
				Kind:    cdiVendor + "/" + cdiClass,
				Devices: []cdiSpec.Device{
					{
						Name: "foodev",
						ContainerEdits: cdiSpec.ContainerEdits{
							Env: []string{
								"FOO=42",
							},
						},
					},
				},
			},
		},
	}

	for _, tcase := range testcases {
		t.Run(tcase.name, func(t *testing.T) {
			logger := testr.New(t)
			saveCDIDir := cdiSpecDir
			t.Cleanup(func() {
				cdiSpecDir = saveCDIDir
			})
			cdiSpecDir = t.TempDir()

			mgr, err := NewCdiManager(logger, testDriverName)
			require.NoError(t, err)

			for _, dev := range tcase.devices {
				err = mgr.AddDevice(logger, dev.name, dev.env)
				require.NoError(t, err)
			}

			got := getSpecFromCache(mgr)
			if diff := cmp.Diff(got, tcase.expectedSpec); diff != "" {
				t.Errorf("unexpected spec: %v", diff)
			}
		})
	}
}

func TestRemoveDevice(t *testing.T) {
	type testcase struct {
		name         string
		initial      []testdevice
		toRemove     []testdevice
		expectedSpec *cdiSpec.Spec
	}

	testcases := []testcase{
		{
			name: "multi device",
			initial: []testdevice{
				{
					name: "foodev",
					env:  "FOO=42",
				},
				{
					name: "bardev",
					env:  "GO=1",
				},
				{
					name: "fizzbuzzdev",
					env:  "SEQ=3,5,15",
				},
			},
			toRemove: []testdevice{
				{
					name: "fizzbuzzdev",
				},
			},
			expectedSpec: &cdiSpec.Spec{
				Version: cdiSpecVersion,
				Kind:    cdiVendor + "/" + cdiClass,
				Devices: []cdiSpec.Device{
					{
						Name: "bardev",
						ContainerEdits: cdiSpec.ContainerEdits{
							Env: []string{
								"GO=1",
							},
						},
					},
					{
						Name: "foodev",
						ContainerEdits: cdiSpec.ContainerEdits{
							Env: []string{
								"FOO=42",
							},
						},
					},
				},
			},
		},
		{
			name: "remove all devices triggers file deletion",
			initial: []testdevice{
				{
					name: "foodev",
					env:  "FOO=42",
				},
			},
			toRemove: []testdevice{
				{
					name: "foodev",
				},
			},
			expectedSpec: nil,
		},
	}

	for _, tcase := range testcases {
		t.Run(tcase.name, func(t *testing.T) {
			logger := testr.New(t)
			saveCDIDir := cdiSpecDir
			t.Cleanup(func() {
				cdiSpecDir = saveCDIDir
			})
			cdiSpecDir = t.TempDir()

			mgr, err := NewCdiManager(logger, testDriverName)
			require.NoError(t, err)
			for _, dev := range tcase.initial {
				err = mgr.AddDevice(logger, dev.name, dev.env)
				require.NoError(t, err)
			}
			for _, dev := range tcase.toRemove {
				err = mgr.RemoveDevice(logger, dev.name)
				require.NoError(t, err)
			}

			got := getSpecFromCache(mgr)
			if diff := cmp.Diff(got, tcase.expectedSpec); diff != "" {
				t.Errorf("unexpected spec: %v", diff)
			}
		})
	}
}

func TestStateRecovery(t *testing.T) {
	logger := testr.New(t)
	saveCDIDir := cdiSpecDir
	t.Cleanup(func() {
		cdiSpecDir = saveCDIDir
	})
	cdiSpecDir = t.TempDir()

	// 1. Start the first manager and allocate a device (simulating initial driver boot)
	mgr1, err := NewCdiManager(logger, testDriverName)
	require.NoError(t, err)

	err = mgr1.AddDevice(logger, "recovered-device", "CPU=0")
	require.NoError(t, err)

	// Verify it actually wrote to the cache
	got1 := getSpecFromCache(mgr1)
	require.NotNil(t, got1)
	require.Len(t, got1.Devices, 1)

	// 2. Start a second manager pointing to the exact same temp directory (simulating a Pod restart)
	mgr2, err := NewCdiManager(logger, testDriverName)
	require.NoError(t, err)

	// 3. Verify the second manager successfully recovered the state from disk
	got2 := getSpecFromCache(mgr2)
	require.NotNil(t, got2)
	if diff := cmp.Diff(got1, got2); diff != "" {
		t.Errorf("recovered spec differs from original: %v", diff)
	}

	// 4. Verify the internal state map recovered correctly by removing the device
	err = mgr2.RemoveDevice(logger, "recovered-device")
	require.NoError(t, err)

	// Verify file deletion triggered successfully after removal
	got3 := getSpecFromCache(mgr2)
	require.Nil(t, got3, "expected spec to be nil after removing the recovered device")
}
