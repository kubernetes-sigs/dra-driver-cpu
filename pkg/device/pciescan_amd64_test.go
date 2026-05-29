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

import "testing"

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
