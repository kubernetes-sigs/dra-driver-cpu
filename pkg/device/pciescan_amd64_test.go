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
	"sort"
	"testing"
	"testing/fstest"

	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	"k8s.io/utils/cpuset"
)

func TestIsPCIeRootName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{
			name:  "valid single-domain zero bus",
			input: "pci0000:00",
			want:  true,
		},
		{
			name:  "valid nonzero domain and bus",
			input: "pci0001:0c",
			want:  true,
		},
		{
			name:  "valid all hex digits",
			input: "pciaabb:ff",
			want:  true,
		},
		{
			name:  "valid high bus number",
			input: "pci0000:f3",
			want:  true,
		},
		{
			name:  "empty string",
			input: "",
			want:  false,
		},
		{
			name:  "too short",
			input: "pci0000:0",
			want:  false,
		},
		{
			name:  "too long",
			input: "pci0000:000",
			want:  false,
		},
		{
			name:  "wrong prefix",
			input: "xxx0000:00",
			want:  false,
		},
		{
			name:  "missing colon",
			input: "pci000000",
			want:  false,
		},
		{
			name:  "uppercase hex",
			input: "pciAABB:FF",
			want:  false,
		},
		{
			name:  "non-hex character in domain",
			input: "pci000g:00",
			want:  false,
		},
		{
			name:  "non-hex character in bus",
			input: "pci0000:0z",
			want:  false,
		},
		{
			name:  "BDF address not a root name",
			input: "0000:00:1f.0",
			want:  false,
		},
		{
			name:  "child bus path fragment",
			input: "pci_bus",
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsPCIeRootName(tt.input)
			if got != tt.want {
				t.Errorf("IsPCIeRootName(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestPCIeDomainsFromFS(t *testing.T) {
	logger := testr.New(t)
	tests := []struct {
		name string
		fs   fstest.MapFS
		want []PCIeDomain
	}{
		{
			name: "empty FS",
			fs: fstest.MapFS{
				"devices": &fstest.MapFile{
					Mode: fs.ModeDir,
				},
			},
			want: []PCIeDomain{},
		},
		{
			name: "ryzen 9 5950X - single PCIe root",
			fs:   makeRyzen95950XFixture(),
			want: []PCIeDomain{
				{
					// created manually inspecting the machine hardware.
					RootName:  "pci0000:00",
					LocalCPUs: mustParseCPUSet(t, "0-31"),
				},
			},
		},
		{
			name: "xeon gold 6230R - multiple symmetric PCIe roots",
			fs:   makeXeonGold6230RFixture(),
			want: []PCIeDomain{
				{
					RootName:  "pci0000:00",
					LocalCPUs: mustParseCPUSet(t, "0,2,4,6,8,10,12,14,16,18,20,22,24,26,28,30,32,34,36,38,40,42,44,46,48,50,52,54,56,58,60,62,64,66,68,70,72,74,76,78,80,82,84,86,88,90,92,94,96,98,100,102"),
				},
				{
					RootName:  "pci0000:17",
					LocalCPUs: mustParseCPUSet(t, "0,2,4,6,8,10,12,14,16,18,20,22,24,26,28,30,32,34,36,38,40,42,44,46,48,50,52,54,56,58,60,62,64,66,68,70,72,74,76,78,80,82,84,86,88,90,92,94,96,98,100,102"),
				},
				{
					RootName:  "pci0000:3a",
					LocalCPUs: mustParseCPUSet(t, "0,2,4,6,8,10,12,14,16,18,20,22,24,26,28,30,32,34,36,38,40,42,44,46,48,50,52,54,56,58,60,62,64,66,68,70,72,74,76,78,80,82,84,86,88,90,92,94,96,98,100,102"),
				},
				{
					RootName:  "pci0000:5d",
					LocalCPUs: mustParseCPUSet(t, "0,2,4,6,8,10,12,14,16,18,20,22,24,26,28,30,32,34,36,38,40,42,44,46,48,50,52,54,56,58,60,62,64,66,68,70,72,74,76,78,80,82,84,86,88,90,92,94,96,98,100,102"),
				},
				{
					RootName:  "pci0000:80",
					LocalCPUs: mustParseCPUSet(t, "1,3,5,7,9,11,13,15,17,19,21,23,25,27,29,31,33,35,37,39,41,43,45,47,49,51,53,55,57,59,61,63,65,67,69,71,73,75,77,79,81,83,85,87,89,91,93,95,97,99,101,103"),
				},
				{
					RootName:  "pci0000:85",
					LocalCPUs: mustParseCPUSet(t, "1,3,5,7,9,11,13,15,17,19,21,23,25,27,29,31,33,35,37,39,41,43,45,47,49,51,53,55,57,59,61,63,65,67,69,71,73,75,77,79,81,83,85,87,89,91,93,95,97,99,101,103"),
				},
				{
					RootName:  "pci0000:ae",
					LocalCPUs: mustParseCPUSet(t, "1,3,5,7,9,11,13,15,17,19,21,23,25,27,29,31,33,35,37,39,41,43,45,47,49,51,53,55,57,59,61,63,65,67,69,71,73,75,77,79,81,83,85,87,89,91,93,95,97,99,101,103"),
				},
				{
					RootName:  "pci0000:d7",
					LocalCPUs: mustParseCPUSet(t, "1,3,5,7,9,11,13,15,17,19,21,23,25,27,29,31,33,35,37,39,41,43,45,47,49,51,53,55,57,59,61,63,65,67,69,71,73,75,77,79,81,83,85,87,89,91,93,95,97,99,101,103"),
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := PCIeDomainsFromFS(logger, tt.fs)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			sort.Slice(got, func(i, j int) bool {
				return got[i].RootName < got[j].RootName
			})
			if diff := cmp.Diff(tt.want, got, cmp.Comparer(func(a, b cpuset.CPUSet) bool {
				return a.Equals(b)
			})); diff != "" {
				t.Errorf("PCIeDomain mismatch:\n%s", diff)
			}
		})
	}
}
