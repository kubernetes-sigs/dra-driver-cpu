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

package device

import (
	"io/fs"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/utils/cpuset"
)

func TestOnlineCPUs(t *testing.T) {
	logger := testr.New(t)
	tests := []struct {
		name    string
		fs      fstest.MapFS
		want    string
		wantErr bool
	}{
		{
			name:    "missing cpu online file",
			fs:      fstest.MapFS{},
			wantErr: true,
		},
		{
			name: "single range",
			fs: fstest.MapFS{
				filepath.Join("devices", "system", "cpu", "online"): &fstest.MapFile{
					Data: []byte("0-255"),
				},
			},
			want: "0-255",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := OnlineCPUs(logger, tt.fs)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			want := mustParseCPUSet(t, tt.want)
			if !got.Equals(want) {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}

func TestFindOrphanedCPUs(t *testing.T) {
	tests := []struct {
		name         string
		domains      []PCIeDomain
		allCPUs      cpuset.CPUSet
		expectedCPUs cpuset.CPUSet
	}{
		{
			name:         "all empty",
			expectedCPUs: cpuset.New(),
		},
		{
			name:         "no domains",
			allCPUs:      mustParseCPUSet(t, "0-15"),
			expectedCPUs: mustParseCPUSet(t, "0-15"),
		},
		{
			name:    "single domain",
			allCPUs: mustParseCPUSet(t, "0-15"),
			domains: []PCIeDomain{
				{
					PCIeRootAttr: newDeviceAttr("pci0000.00"),
					LocalCPUs:    mustParseCPUSet(t, "0-15"),
				},
			},
			expectedCPUs: cpuset.New(),
		},
		{
			name:    "even split across domain",
			allCPUs: mustParseCPUSet(t, "0-15"),
			domains: []PCIeDomain{
				{
					PCIeRootAttr: newDeviceAttr("pci0000.00"),
					LocalCPUs:    mustParseCPUSet(t, "0-7"),
				},
				{
					PCIeRootAttr: newDeviceAttr("pci0001.00"),
					LocalCPUs:    mustParseCPUSet(t, "8-15"),
				},
			},
			expectedCPUs: cpuset.New(),
		},
		{
			name:    "leaking cores", // used random IDs
			allCPUs: mustParseCPUSet(t, "0-15"),
			domains: []PCIeDomain{
				{
					PCIeRootAttr: newDeviceAttr("pci0000.00"),
					LocalCPUs:    mustParseCPUSet(t, "0-6"),
				},
				{
					PCIeRootAttr: newDeviceAttr("pci0001.00"),
					LocalCPUs:    mustParseCPUSet(t, "8-14"),
				},
			},
			expectedCPUs: cpuset.New(7, 15),
		},
		{
			name:    "overlapping domains", // this is wrong but this function doesn't detect it by design
			allCPUs: mustParseCPUSet(t, "0-15"),
			domains: []PCIeDomain{
				{
					PCIeRootAttr: newDeviceAttr("pci0000.00"),
					LocalCPUs:    mustParseCPUSet(t, "0-8"),
				},
				{
					PCIeRootAttr: newDeviceAttr("pci0001.00"),
					LocalCPUs:    mustParseCPUSet(t, "7-15"),
				},
			},
			expectedCPUs: cpuset.New(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FindOrphanedCPUs(tt.domains, tt.allCPUs)
			if !tt.expectedCPUs.Equals(got) {
				t.Errorf("got=%s expected=%s", got.String(), tt.expectedCPUs.String())
			}
		})
	}
}

func newDeviceAttr(pcidom string) deviceattribute.DeviceAttribute {
	return deviceattribute.DeviceAttribute{
		Name:  deviceattribute.StandardDeviceAttributePCIeRoot,
		Value: resourceapi.DeviceAttribute{StringValue: &pcidom},
	}
}

func mapFSFromDevices(pciDevs []PCIeDevice) fstest.MapFS {
	return fstest.MapFS{
		filepath.Join("bus", "pci", "devices"): &fstest.MapFile{
			Mode: fs.ModeDir,
		},
	}
}

// cpuSetComparer lets cmp.Diff use cpuset.CPUSet.Equals, avoiding unexported values.
var cpuSetComparer = cmp.Comparer(func(a, b cpuset.CPUSet) bool {
	return a.Equals(b)
})

func mustParseCPUSet(t *testing.T, s string) cpuset.CPUSet {
	t.Helper()
	cpus, err := cpuset.Parse(s)
	if err != nil {
		t.Fatalf("parsing cpuset %q: %v", s, err)
	}
	return cpus
}
