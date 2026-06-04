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
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/buildinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
)

type DRACPUBuildinfo struct {
	GoVersion   string `json:"goVersion"`
	VCSRevision string `json:"vcsRevision"`
	VCSTime     string `json:"vcsTime"`
}

type DRACPUAllocation struct {
	CPUs string `json:"cpus"`
}

type DRACPURuntimeinfo struct {
	CPUAffinity string `json:"affinity"`
}

type DRACPUInfo struct {
	Buildinfo DRACPUBuildinfo   `json:"buildinfo"`
	CPUs      []cpuinfo.CPUInfo `json:"cpus"`
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
