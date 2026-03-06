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

package cpuinfo

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/cpuset"
)

var testCPUDetails = CPUDetails{
	0: {CpuID: 0, CoreID: 0, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
	1: {CpuID: 1, CoreID: 0, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
	2: {CpuID: 2, CoreID: 1, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
	3: {CpuID: 3, CoreID: 1, SocketID: 0, NUMANodeID: 0, UncoreCacheID: 0},
	4: {CpuID: 4, CoreID: 2, SocketID: 1, NUMANodeID: 1, UncoreCacheID: 1},
	5: {CpuID: 5, CoreID: 2, SocketID: 1, NUMANodeID: 1, UncoreCacheID: 1},
	6: {CpuID: 6, CoreID: 3, SocketID: 1, NUMANodeID: 1, UncoreCacheID: 1},
	7: {CpuID: 7, CoreID: 3, SocketID: 1, NUMANodeID: 1, UncoreCacheID: 1},
}

func TestKeepOnly(t *testing.T) {
	tests := []struct {
		name string
		cpus cpuset.CPUSet
		want CPUDetails
	}{
		{
			name: "partial overlap",
			cpus: cpuset.New(0, 1, 4, 5, 12),
			want: CPUDetails{
				0: testCPUDetails[0],
				1: testCPUDetails[1],
				4: testCPUDetails[4],
				5: testCPUDetails[5],
			},
		},
		{
			name: "no overlap",
			cpus: cpuset.New(100, 101),
			want: CPUDetails{},
		},
		{
			name: "empty set",
			cpus: cpuset.New(),
			want: CPUDetails{},
		},
		{
			name: "full overlap",
			cpus: testCPUDetails.CPUs(),
			want: testCPUDetails,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := testCPUDetails.KeepOnly(tt.cpus)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("KeepOnly() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCPUs(t *testing.T) {
	assert.True(t, cpuset.New(0, 1, 2, 3, 4, 5, 6, 7).Equals(testCPUDetails.CPUs()))
}

func TestCPUsInNUMANodes(t *testing.T) {
	assert.True(t, cpuset.New(0, 1, 2, 3).Equals(testCPUDetails.CPUsInNUMANodes(0)))
	assert.True(t, cpuset.New(4, 5, 6, 7).Equals(testCPUDetails.CPUsInNUMANodes(1)))
	assert.True(t, cpuset.New(0, 1, 2, 3, 4, 5, 6, 7).Equals(testCPUDetails.CPUsInNUMANodes(0, 1)))
	assert.True(t, cpuset.New().Equals(testCPUDetails.CPUsInNUMANodes(2)))
}

func TestCPUsInCores(t *testing.T) {
	assert.True(t, cpuset.New(0, 1).Equals(testCPUDetails.CPUsInCores(0)))
	assert.True(t, cpuset.New(4, 5).Equals(testCPUDetails.CPUsInCores(2)))
	assert.True(t, cpuset.New(0, 1, 4, 5).Equals(testCPUDetails.CPUsInCores(0, 2)))
	assert.True(t, cpuset.New().Equals(testCPUDetails.CPUsInCores(10)))
}

func TestCPUsInSockets(t *testing.T) {
	assert.True(t, cpuset.New(0, 1, 2, 3).Equals(testCPUDetails.CPUsInSockets(0)))
	assert.True(t, cpuset.New(4, 5, 6, 7).Equals(testCPUDetails.CPUsInSockets(1)))
	assert.True(t, cpuset.New().Equals(testCPUDetails.CPUsInSockets(2)))
}

func TestCPUsInUncoreCaches(t *testing.T) {
	assert.True(t, cpuset.New(0, 1, 2, 3).Equals(testCPUDetails.CPUsInUncoreCaches(0)))
	assert.True(t, cpuset.New(4, 5, 6, 7).Equals(testCPUDetails.CPUsInUncoreCaches(1)))
	assert.True(t, cpuset.New().Equals(testCPUDetails.CPUsInUncoreCaches(2)))
}

func TestNUMANodes(t *testing.T) {
	assert.True(t, cpuset.New(0, 1).Equals(testCPUDetails.NUMANodes()))
}

func TestSockets(t *testing.T) {
	assert.True(t, cpuset.New(0, 1).Equals(testCPUDetails.Sockets()))
}

func TestUnCoresInNUMANodes(t *testing.T) {
	assert.True(t, cpuset.New(0).Equals(testCPUDetails.UncoreInNUMANodes(0)))
	assert.True(t, cpuset.New(1).Equals(testCPUDetails.UncoreInNUMANodes(1)))
	assert.True(t, cpuset.New().Equals(testCPUDetails.UncoreInNUMANodes(2)))
}

func TestSocketsInNUMANodes(t *testing.T) {
	assert.True(t, cpuset.New(0).Equals(testCPUDetails.SocketsInNUMANodes(0)))
	assert.True(t, cpuset.New(1).Equals(testCPUDetails.SocketsInNUMANodes(1)))
}

func TestCoresInSockets(t *testing.T) {
	assert.True(t, cpuset.New(0, 1).Equals(testCPUDetails.CoresInSockets(0)))
	assert.True(t, cpuset.New(2, 3).Equals(testCPUDetails.CoresInSockets(1)))
	assert.True(t, cpuset.New().Equals(testCPUDetails.CoresInSockets(2)))
}

func TestNUMANodesInSockets(t *testing.T) {
	assert.True(t, cpuset.New(0).Equals(testCPUDetails.NUMANodesInSockets(0)))
	assert.True(t, cpuset.New(1).Equals(testCPUDetails.NUMANodesInSockets(1)))
}

func TestCoresInNUMANodes(t *testing.T) {
	assert.True(t, cpuset.New(0, 1).Equals(testCPUDetails.CoresInNUMANodes(0)))
	assert.True(t, cpuset.New(2, 3).Equals(testCPUDetails.CoresInNUMANodes(1)))
}

func TestCoresNeededInUncoreCache(t *testing.T) {
	assert.True(t, cpuset.New(0, 1).Equals(testCPUDetails.CoresNeededInUncoreCache(5, 0)))
	assert.True(t, cpuset.New(0).Equals(testCPUDetails.CoresNeededInUncoreCache(1, 0)))
	assert.True(t, cpuset.New(2, 3).Equals(testCPUDetails.CoresNeededInUncoreCache(2, 1)))
	// Non-existent uncore ID
	assert.True(t, cpuset.New().Equals(testCPUDetails.CoresNeededInUncoreCache(3, 2)))
}

func TestCoresInUncoreCache(t *testing.T) {
	assert.True(t, cpuset.New(0, 1).Equals(testCPUDetails.CoresInUncoreCache(0)))
	assert.True(t, cpuset.New(2, 3).Equals(testCPUDetails.CoresInUncoreCache(1)))
	assert.True(t, cpuset.New().Equals(testCPUDetails.CoresInUncoreCache(2)))
}
