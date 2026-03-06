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
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/require"
	cdiSpec "tags.cncf.io/container-device-interface/specs-go"
)

type testdevice struct {
	name string
	env  string
}

func TestAddDevice(t *testing.T) {
	type testcase struct {
		name         string
		devices      []testdevice
		expectedSpec *cdiSpec.Spec
	}

	testcases := []testcase{
		{
			name: "empty",
			expectedSpec: &cdiSpec.Spec{
				Version: cdiSpecVersion,
				Kind:    fmt.Sprintf("%s/%s", cdiVendor, cdiClass),
				Devices: []cdiSpec.Device{},
			},
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
				Kind:    fmt.Sprintf("%s/%s", cdiVendor, cdiClass),
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
			saveCDIDir := cdiSpecDir
			t.Cleanup(func() {
				cdiSpecDir = saveCDIDir
			})
			cdiSpecDir = t.TempDir()

			mgr, err := NewCdiManager(testDriverName)
			require.NoError(t, err)

			_, err = os.Stat(filepath.Join(cdiSpecDir, fmt.Sprintf("%s.json", testDriverName)))
			require.NoError(t, err)

			for _, dev := range tcase.devices {
				err = mgr.AddDevice(dev.name, dev.env)
				require.NoError(t, err)
			}

			got, err := mgr.GetSpec()
			require.NoError(t, err)
			if diff := cmp.Diff(got, tcase.expectedSpec); diff != "" {
				t.Errorf("unexpected spec from empty: %v", diff)
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
				Kind:    fmt.Sprintf("%s/%s", cdiVendor, cdiClass),
				Devices: []cdiSpec.Device{
					{
						Name: "foodev",
						ContainerEdits: cdiSpec.ContainerEdits{
							Env: []string{
								"FOO=42",
							},
						},
					},
					{
						Name: "bardev",
						ContainerEdits: cdiSpec.ContainerEdits{
							Env: []string{
								"GO=1",
							},
						},
					},
				},
			},
		},
	}

	for _, tcase := range testcases {
		t.Run(tcase.name, func(t *testing.T) {
			saveCDIDir := cdiSpecDir
			t.Cleanup(func() {
				cdiSpecDir = saveCDIDir
			})
			cdiSpecDir = t.TempDir()

			mgr, err := NewCdiManager(testDriverName)
			require.NoError(t, err)
			for _, dev := range tcase.initial {
				err = mgr.AddDevice(dev.name, dev.env)
				require.NoError(t, err)
			}
			for _, dev := range tcase.toRemove {
				err = mgr.RemoveDevice(dev.name)
				require.NoError(t, err)
			}

			got, err := mgr.GetSpec()
			require.NoError(t, err)
			if diff := cmp.Diff(got, tcase.expectedSpec); diff != "" {
				t.Errorf("unexpected spec from empty: %v", diff)
			}
		})
	}
}
