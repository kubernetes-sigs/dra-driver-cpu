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

package discovery

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"

	dracpuapi "github.com/kubernetes-sigs/dra-driver-cpu/api"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/buildinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"k8s.io/utils/cpuset"
)

var (
	ErrNotFound = errors.New("not found")
)

type DRACPUBuildinfo struct {
	GoVersion   string `json:"goVersion"`
	VCSRevision string `json:"vcsRevision"`
	VCSTime     string `json:"vcsTime"`
}

type DRACPUAllocation struct {
	CPUs string `json:"cpus"`
}

type DRACPUEnvVar struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type DRACPUEnvironment struct {
	Vars []DRACPUEnvVar `json:"vars"`
}

func (env DRACPUEnvironment) AssignedCPUSet() (cpuset.CPUSet, error) {
	for _, ev := range env.Vars {
		if ev.Name == dracpuapi.EnvVarExclusiveAssignedCPUSet {
			cpus, err := cpuset.Parse(ev.Value)
			if err != nil {
				return cpuset.New(), fmt.Errorf("invalid variable: %w", err)
			}
			return cpus, nil
		}
	}
	return cpuset.New(), ErrNotFound
}

func FromEnviron() DRACPUEnvironment {
	ret := DRACPUEnvironment{
		Vars: []DRACPUEnvVar{},
	}
	if val, ok := os.LookupEnv(dracpuapi.EnvVarExclusiveAssignedCPUSet); ok {
		ret.Vars = append(ret.Vars, DRACPUEnvVar{
			Name:  dracpuapi.EnvVarExclusiveAssignedCPUSet,
			Value: val,
		})
	}
	return ret
}

type DRACPURuntimeinfo struct {
	CPUAffinity string            `json:"affinity"`
	Environ     DRACPUEnvironment `json:"environ"`
}

type DRACPUInfo struct {
	Buildinfo DRACPUBuildinfo   `json:"buildinfo"`
	CPUs      []cpuinfo.CPUInfo `json:"cpus"`
}

type DRACPUNUMAInfo struct {
	SocketID   int
	NUMANodeID int
	CPUs       cpuset.CPUSet
}

func (ci DRACPUInfo) BySocket() map[int]cpuset.CPUSet {
	ret := make(map[int]cpuset.CPUSet)
	for _, cpu := range ci.CPUs {
		cur, ok := ret[cpu.SocketID]
		if !ok {
			cur = cpuset.New()
		}
		cur = cur.Union(cpuset.New(cpu.CpuID))
		ret[cpu.SocketID] = cur
	}
	return ret
}

func (ci DRACPUInfo) ByNUMANode() map[int]DRACPUNUMAInfo {
	tmp := make(map[int]DRACPUNUMAInfo)
	for _, cpu := range ci.CPUs {
		// systems with 8192 or more socket would already
		// long broken the system in multiple places.
		// 8192 is a random "high enough" value
		key := cpu.SocketID*8192 + cpu.NUMANodeID
		tmp[key] = DRACPUNUMAInfo{
			SocketID:   cpu.SocketID,
			NUMANodeID: cpu.NUMANodeID,
			CPUs:       cpu.NumaNodeCPUSet,
		}
	}
	nid := 0 // user friendlier computed unique ID
	// we need 2 steps because NUMA Node is guaranteed unique
	// by socket ID.
	ret := make(map[int]DRACPUNUMAInfo)
	keys := slices.Sorted(maps.Keys(tmp))
	for _, key := range keys {
		ret[nid] = tmp[key]
		nid++
	}
	return ret
}

type DRACPUTester struct {
	Buildinfo   DRACPUBuildinfo   `json:"buildinfo"`
	Allocation  DRACPUAllocation  `json:"allocation"`
	Runtimeinfo DRACPURuntimeinfo `json:"runtimeinfo"`
}

func NewBuildinfo() DRACPUBuildinfo {
	info := buildinfo.Read()
	return DRACPUBuildinfo{
		GoVersion:   info.GoVersion,
		VCSRevision: info.VCSRevision,
		VCSTime:     info.VCSTime,
	}
}
