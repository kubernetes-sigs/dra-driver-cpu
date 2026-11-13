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
package memory_test

import (
	"testing"
	"testing/fstest"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/memory"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func TestDiscover(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  []memory.Device
		err   string
	}{
		{name: "memory-only NUMA node", files: map[string]string{"online": "2", "node2/meminfo": "Node 2 MemTotal: 8192 kB\n"}, want: []memory.Device{{Kind: "memory", NUMA: 2, Bytes: 8 << 20, PageSize: 4096}}},
		{name: "subtract all hugepage sizes", files: map[string]string{"online": "0", "node0/meminfo": "Node 0 MemTotal: 4194304 kB\n", "node0/hugepages/hugepages-2048kB/nr_hugepages": "2", "node0/hugepages/hugepages-1048576kB/nr_hugepages": "1", "node0/hugepages/hugepages-64kB/nr_hugepages": "1"}, want: []memory.Device{{Kind: "memory", NUMA: 0, Bytes: (3 << 30) - (4 << 20) - (64 << 10), PageSize: 4096}, {Kind: "hugepages-1g", NUMA: 0, Bytes: 1 << 30, PageSize: 1 << 30}, {Kind: "hugepages-2m", NUMA: 0, Bytes: 4 << 20, PageSize: 2 << 20}}},
		{name: "zero hugepages", files: map[string]string{"online": "0", "node0/meminfo": "Node 0 MemTotal: 4096 kB", "node0/hugepages/hugepages-2048kB/nr_hugepages": "0"}, want: []memory.Device{{Kind: "memory", NUMA: 0, Bytes: 4 << 20, PageSize: 4096}}},
		{name: "missing online", err: "read NUMA online"},
		{name: "invalid online", files: map[string]string{"online": "bad"}, err: "parse NUMA online"},
		{name: "invalid total", files: map[string]string{"online": "0", "node0/meminfo": "Node 0 MemTotal: -1 kB"}, err: "invalid"},
		{name: "inconsistent total", files: map[string]string{"online": "0", "node0/meminfo": "Node 0 MemTotal: 1024 kB", "node0/hugepages/hugepages-2048kB/nr_hugepages": "1"}, err: "exceeds MemTotal"},
		{name: "invalid page count", files: map[string]string{"online": "0", "node0/meminfo": "Node 0 MemTotal: 4096 kB", "node0/hugepages/hugepages-2048kB/nr_hugepages": "-1"}, err: "invalid page count"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fs := fstest.MapFS{}
			for name, data := range tc.files {
				fs["devices/system/node/"+name] = &fstest.MapFile{Data: []byte(data)}
			}
			got, err := memory.Discover(fs)
			if tc.err != "" {
				require.ErrorContains(t, err, tc.err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}

func TestDeviceResource(t *testing.T) {
	for _, tc := range []struct {
		kind, native, size string
		bytes, page        int64
	}{
		{"memory", "memory", "4Mi", 4 << 20, 4096},
		{"hugepages-2m", "hugepages-2Mi", "4Mi", 4 << 20, 2 << 20},
		{"hugepages-1g", "hugepages-1Gi", "1Gi", 1 << 30, 1 << 30},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			d := memory.Device{Kind: tc.kind, NUMA: 1, Bytes: tc.bytes, PageSize: tc.page}
			r := d.Resource()
			require.Equal(t, tc.kind+"-numa1", r.Name)
			require.Equal(t, "worker/"+tc.kind, d.Pool("worker"))
			require.Equal(t, tc.kind, *r.Attributes[memory.ResourceType].StringValue)
			require.Equal(t, int64(1), *r.Attributes["resource.kubernetes.io/numaNode"].IntValue)
			capacity := r.Capacity[memory.Size]
			require.Zero(t, capacity.Value.Cmp(resource.MustParse(tc.size)))
			for native, mapping := range r.NodeAllocatableResources {
				require.Equal(t, tc.native, string(native))
				require.Equal(t, memory.Size, *mapping.Mapping.CapacityKey)
			}
			require.Len(t, r.NodeAllocatableResources, 1)
			require.True(t, *r.AllowMultipleAllocations)
			if tc.bytes == tc.page {
				require.Len(t, capacity.RequestPolicy.ValidValues, 1)
				require.Nil(t, capacity.RequestPolicy.ValidRange)
			} else {
				require.NotNil(t, capacity.RequestPolicy.ValidRange)
			}
		})
	}
}

func TestConsumed(t *testing.T) {
	for _, tc := range []struct {
		name, key, value string
		want             int64
		bad              bool
	}{
		{"qualified", "dra.cpu/size", "4Mi", 4 << 20, false},
		{"unqualified", "size", "2Mi", 2 << 20, false},
		{"missing", "other", "2Mi", 0, true},
		{"zero", "size", "0", 0, true},
		{"fraction", "size", "0.5", 0, true},
		{"unaligned", "size", "3Mi", 0, true},
		{"too large", "size", "8Mi", 0, true},
		{"overflow", "size", "999999999999999999999", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := resourceapi.DeviceRequestAllocationResult{Device: "hp", ConsumedCapacity: map[resourceapi.QualifiedName]resource.Quantity{resourceapi.QualifiedName(tc.key): resource.MustParse(tc.value)}}
			got, err := memory.Consumed(result, "dra.cpu", memory.Device{Bytes: 4 << 20, PageSize: 2 << 20})
			if tc.bad {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, got)
			}
		})
	}
}
