//go:build linux

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
package main

import (
	"testing"
	"testing/fstest"

	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/discovery"
	"github.com/stretchr/testify/require"
)

func TestMemoryReport(t *testing.T) {
	for _, tc := range []struct {
		name, missing string
		want          discovery.MemoryProbe
		wantErr       string
	}{
		{name: "both sizes", want: discovery.MemoryProbe{CPUs: "2-3", Mems: "1", MemoryMax: "67108864", Huge2MMax: "2097152", Huge1GMax: "0"}},
		{name: "2 MiB only", missing: "hugetlb.1GB.max", want: discovery.MemoryProbe{CPUs: "2-3", Mems: "1", MemoryMax: "67108864", Huge2MMax: "2097152"}},
		{name: "missing memory controller", missing: "memory.max", wantErr: "memory.max"},
		{name: "missing 2 MiB controller", missing: "hugetlb.2MB.max", wantErr: "hugetlb.2MB.max"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sfs := fstest.MapFS{}
			for name, data := range map[string]string{"cpuset.cpus.effective": "2-3\n", "cpuset.mems.effective": "1\n", "memory.max": "67108864\n", "hugetlb.2MB.max": "2097152\n", "hugetlb.1GB.max": "0\n"} {
				if name != tc.missing {
					sfs[name] = &fstest.MapFile{Data: []byte(data)}
				}
			}
			got, err := memoryReport(sfs)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
