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
	"sort"
	"testing"
	"testing/fstest"

	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	"k8s.io/utils/cpuset"
)

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
				filepath.Join("bus", "pci", "devices"): &fstest.MapFile{
					Mode: fs.ModeDir,
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
				return *got[i].PCIeRootAttr.Value.StringValue < *got[j].PCIeRootAttr.Value.StringValue
			})
			if diff := cmp.Diff(tt.want, got, cmp.Comparer(func(a, b cpuset.CPUSet) bool {
				return a.Equals(b)
			})); diff != "" {
				t.Errorf("PCIeDomain mismatch:\n%s", diff)
			}
		})
	}
}
