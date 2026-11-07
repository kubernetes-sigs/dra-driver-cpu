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

package driverconfig

import "testing"

func TestValidate(t *testing.T) {
	for _, tc := range []struct {
		name        string
		conf        Config
		expectedErr bool
	}{{
		name: "error: unknown allocator",
		conf: Config{
			KubeletRootDir:     "/var/lib/kubelet",
			CPUDeviceMode:      "grouped",
			GroupBy:            "numanode",
			Allocator:          "other-allocator",
			ReservedCPUsPolicy: "strict",
		},
		expectedErr: true,
	}, {
		name: "error: non-external allocator in machine mode",
		conf: Config{
			KubeletRootDir:     "/var/lib/kubelet",
			CPUDeviceMode:      "grouped",
			GroupBy:            "machine",
			Allocator:          "cpumanager",
			ReservedCPUsPolicy: "strict",
		},
		expectedErr: true,
	}, {
		name: "valid: external allocator in machine mode",
		conf: Config{
			KubeletRootDir:     "/var/lib/kubelet",
			CPUDeviceMode:      "grouped",
			GroupBy:            "machine",
			Allocator:          "external",
			ReservedCPUsPolicy: "strict",
		},
		expectedErr: false,
	}, {
		name: "valid: allocator: cpumanager",
		conf: Config{
			KubeletRootDir:     "/var/lib/kubelet",
			CPUDeviceMode:      "grouped",
			GroupBy:            "numanode",
			Allocator:          "cpumanager",
			ReservedCPUsPolicy: "strict",
		},
		expectedErr: false,
	}, {
		name: "valid: allocator: external",
		conf: Config{
			KubeletRootDir:     "/var/lib/kubelet",
			CPUDeviceMode:      "grouped",
			GroupBy:            "numanode",
			Allocator:          "external",
			ReservedCPUsPolicy: "strict",
		},
		expectedErr: false,
	}, {
		name: "error: unknown reservedCPUsPolicy",
		conf: Config{
			KubeletRootDir:     "/var/lib/kubelet",
			CPUDeviceMode:      "grouped",
			GroupBy:            "numanode",
			Allocator:          "cpumanager",
			ReservedCPUs:       "0-1",
			ReservedCPUsPolicy: "other-policy",
		},
		expectedErr: true,
	}, {
		name: "error: empty reservedCPUsPolicy",
		conf: Config{
			KubeletRootDir: "/var/lib/kubelet",
			CPUDeviceMode:  "grouped",
			GroupBy:        "numanode",
			Allocator:      "cpumanager",
		},
		expectedErr: true,
	}, {
		name: "error: reservedCPUsPolicy shared without reservedCPUs",
		conf: Config{
			KubeletRootDir:     "/var/lib/kubelet",
			CPUDeviceMode:      "grouped",
			GroupBy:            "numanode",
			Allocator:          "cpumanager",
			ReservedCPUsPolicy: "shared",
		},
		expectedErr: true,
	}, {
		name: "valid: reservedCPUsPolicy shared with reservedCPUs",
		conf: Config{
			KubeletRootDir:     "/var/lib/kubelet",
			CPUDeviceMode:      "grouped",
			GroupBy:            "numanode",
			Allocator:          "cpumanager",
			ReservedCPUs:       "0-1",
			ReservedCPUsPolicy: "shared",
		},
		expectedErr: false,
	}, {
		name: "valid: reservedCPUsPolicy strict without reservedCPUs",
		conf: Config{
			KubeletRootDir:     "/var/lib/kubelet",
			CPUDeviceMode:      "grouped",
			GroupBy:            "numanode",
			Allocator:          "cpumanager",
			ReservedCPUsPolicy: "strict",
		},
		expectedErr: false,
	}} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.conf.Validate()
			gotErr := (err != nil)
			if gotErr != tc.expectedErr {
				t.Errorf("error got=%v expected=%v (err=%v)", gotErr, tc.expectedErr, err)
			}
		})
	}
}
