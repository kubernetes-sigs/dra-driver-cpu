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
	"fmt"
	"io/fs"

	"k8s.io/utils/cpuset"
)

const (
	SysfsRoot = "/sys"
)

func FindOrphanedCPUs(domains []PCIeDomain, allCPUs cpuset.CPUSet) cpuset.CPUSet {
	orphanedCPUs := allCPUs.Clone()
	for _, dom := range domains {
		orphanedCPUs = orphanedCPUs.Difference(dom.LocalCPUs)
	}
	return orphanedCPUs
}

// MapCPUsToPCIeDomain returns a map cpuid -> pciedomains
func MapCPUsToPCIeDomain(domains []PCIeDomain, allCPUs cpuset.CPUSet) map[int][]*PCIeDomain {
	ret := make(map[int][]*PCIeDomain, allCPUs.Size())
	for _, cpuid := range allCPUs.UnsortedList() {
		for idx := range domains {
			dom := &domains[idx]
			if !dom.LocalCPUs.Contains(cpuid) {
				continue
			}
			ret[cpuid] = append(ret[cpuid], dom)
		}
	}
	return ret
}

type PCIeDomain struct {
	RootName  string
	LocalCPUs cpuset.CPUSet
}

func (pcd PCIeDomain) String() string {
	return fmt.Sprintf("<PCIeRoot=%s CPUs=%s>", pcd.RootName, pcd.LocalCPUs.String())
}

type SysFS interface {
	fs.ReadLinkFS
	fs.ReadDirFS
}
