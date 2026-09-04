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
	"strconv"
	"strings"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"k8s.io/utils/cpuset"
)

func MakeSMTMap(topo *cpuinfo.CPUTopology) string {
	strides := make(map[int][]int) // stride -> cpuIDs
	for _, info := range topo.CPUDetails {
		stride := cpuSMTStride(info)
		cpus, ok := strides[stride]
		if !ok {
			cpus = []int{}
		}
		strides[stride] = append(cpus, info.CpuID, info.SiblingCPUID)
	}
	// step 1: strided sets
	var sb strings.Builder
	for stride, cpuIDs := range strides {
		if stride == 0 {
			continue
		}
		cpus := cpuset.New(cpuIDs...)
		sb.WriteString(",")
		sb.WriteString(cpus.String())
		sb.WriteString(":")
		sb.WriteString(strconv.Itoa(stride))
	}
	// step 2: non-strided (aka stranded) set
	if strandedCPUs, ok := strides[0]; ok {
		sb.WriteString(",")
		cpus := cpuset.New(strandedCPUs...)
		sb.WriteString(cpus.String())
	}
	// step 3: final validation and encoding
	smtMap := sb.String()
	if len(smtMap) == 0 {
		return ""
	}
	return smtMap[1:] // strip leading stray ","
}
