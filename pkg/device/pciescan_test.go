/*
Copyright 2026 The Kubernetes Authors.

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
	"sort"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/deviceattribute"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/utils/cpuset"
	"k8s.io/utils/ptr"
)

func TestOnlineCPUs(t *testing.T) {
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
			got, err := OnlineCPUs(tt.fs)
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
		domains      []PCIEDomain
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
			domains: []PCIEDomain{
				{
					PCIERootAttr: newDeviceAttr("pci0000.00"),
					LocalCPUs:    mustParseCPUSet(t, "0-15"),
				},
			},
			expectedCPUs: cpuset.New(),
		},
		{
			name:    "even split across domain",
			allCPUs: mustParseCPUSet(t, "0-15"),
			domains: []PCIEDomain{
				{
					PCIERootAttr: newDeviceAttr("pci0000.00"),
					LocalCPUs:    mustParseCPUSet(t, "0-7"),
				},
				{
					PCIERootAttr: newDeviceAttr("pci0001.00"),
					LocalCPUs:    mustParseCPUSet(t, "8-15"),
				},
			},
			expectedCPUs: cpuset.New(),
		},
		{
			name:    "leaking cores", // used random IDs
			allCPUs: mustParseCPUSet(t, "0-15"),
			domains: []PCIEDomain{
				{
					PCIERootAttr: newDeviceAttr("pci0000.00"),
					LocalCPUs:    mustParseCPUSet(t, "0-6"),
				},
				{
					PCIERootAttr: newDeviceAttr("pci0001.00"),
					LocalCPUs:    mustParseCPUSet(t, "8-14"),
				},
			},
			expectedCPUs: cpuset.New(7, 15),
		},
		{
			name:    "overlapping domains", // this is wrong but this function doesn't detect it by design
			allCPUs: mustParseCPUSet(t, "0-15"),
			domains: []PCIEDomain{
				{
					PCIERootAttr: newDeviceAttr("pci0000.00"),
					LocalCPUs:    mustParseCPUSet(t, "0-8"),
				},
				{
					PCIERootAttr: newDeviceAttr("pci0001.00"),
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

func TestPCIEDomainsFromFS(t *testing.T) {
	tests := []struct {
		name    string
		fs      fstest.MapFS
		want    []PCIEDomain
		wantErr bool
	}{
		{
			name: "empty FS",
			fs:   mapFSFromDevices([]PCIEDevice{}),
		},
		{
			name: "laptop single PCIe root",
			fs:   makeBasePCSysFSFixture(),
			want: []PCIEDomain{
				{
					PCIERootAttr: deviceattribute.DeviceAttribute{
						Name:  deviceattribute.StandardDeviceAttributePCIeRoot,
						Value: resourceapi.DeviceAttribute{StringValue: ptr.To("pci0000:00")},
					},
					LocalCPUs: cpuset.New(0, 1, 2, 3, 4, 5, 6, 7),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PCIEDomainsFromFS(tt.fs)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			sort.Slice(got, func(i, j int) bool {
				return *got[i].PCIERootAttr.Value.StringValue < *got[j].PCIERootAttr.Value.StringValue
			})
			if diff := cmp.Diff(tt.want, got, cpuSetComparer); diff != "" {
				t.Errorf("PCIEDomain mismatch (-want +got):\n%s", diff)
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

func mapFSFromDevices(pciDevs []PCIEDevice) fstest.MapFS {
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
