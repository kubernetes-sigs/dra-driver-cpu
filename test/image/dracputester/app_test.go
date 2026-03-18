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

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"k8s.io/utils/cpuset"
)

type maskFromSet struct{ cpus cpuset.CPUSet }

func (m maskFromSet) IsSet(i int) bool { return m.cpus.Contains(i) }

func TestAffinityFromMask(t *testing.T) {
	tests := []struct {
		name     string
		mask     affinityMask
		maxCPUID int
		want     cpuset.CPUSet
	}{
		{
			name:     "empty",
			mask:     maskFromSet{cpuset.New()},
			maxCPUID: 0,
			want:     cpuset.New(),
		},
		{
			name:     "single CPU",
			mask:     maskFromSet{cpuset.New(3)},
			maxCPUID: 0,
			want:     cpuset.New(3),
		},
		{
			name:     "split set 2-5 and 9-13 (bug case: high IDs beyond NumCPU())",
			mask:     maskFromSet{mustParse(t, "2-5,9-13")},
			maxCPUID: 0,
			want:     mustParse(t, "2-5,9-13"),
		},
		{
			name:     "high CPU IDs only",
			mask:     maskFromSet{cpuset.New(100, 101, 200)},
			maxCPUID: 0,
			want:     cpuset.New(100, 101, 200),
		},
		{
			name:     "explicit bound 20",
			mask:     maskFromSet{cpuset.New(2, 5, 9)},
			maxCPUID: 20,
			want:     cpuset.New(2, 5, 9),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := affinityFromMask(tt.mask, tt.maxCPUID)
			if !got.Equals(tt.want) {
				t.Errorf("affinityFromMask() = %s, want %s", got.String(), tt.want.String())
			}
		})
	}
}

func TestAffinityScanBoundFromTopology(t *testing.T) {
	tests := []struct {
		name string
		topo *cpuinfo.CPUTopology
		want int
	}{
		{
			name: "nil returns default",
			topo: nil,
			want: affinityScanMax,
		},
		{
			name: "empty CPUDetails uses affinityScanMax",
			topo: &cpuinfo.CPUTopology{NumCPUs: 16, CPUDetails: cpuinfo.CPUDetails{}},
			want: affinityScanMax,
		},
		{
			name: "max CPU ID plus one",
			topo: &cpuinfo.CPUTopology{
				NumCPUs:    4,
				CPUDetails: cpuinfo.CPUDetails{2: cpuinfo.CPUInfo{}, 3: cpuinfo.CPUInfo{}, 5: cpuinfo.CPUInfo{}, 9: cpuinfo.CPUInfo{}},
			},
			want: 10,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := affinityScanBoundFromTopology(tt.topo); got != tt.want {
				t.Errorf("affinityScanBoundFromTopology() = %d, want %d", got, tt.want)
			}
		})
	}
}

func mustParse(t *testing.T, s string) cpuset.CPUSet {
	t.Helper()
	cpus, err := cpuset.Parse(s)
	if err != nil {
		t.Fatalf("cpuset.Parse(%q): %v", s, err)
	}
	return cpus
}
