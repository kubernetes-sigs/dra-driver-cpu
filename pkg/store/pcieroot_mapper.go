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

package store

import (
	"fmt"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/sysfs"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/cpuset"
)

// PCIeRootMapper maps a logical cpu by its ID to the PCIe roots closest to it.
// Meant to be initialized once at startup and consumed by readers, possibly concurrently.
type PCIeRootMapper struct {
	pcieDomains       []device.PCIeDomain
	cpuIDToPCIeDomain map[int][]*device.PCIeDomain
}

func NewPCIeRootMapper() *PCIeRootMapper {
	return &PCIeRootMapper{}
}

// Probe scans the machine and builds the CPU -> PCIe root mapping
func (prm *PCIeRootMapper) Probe(logger logr.Logger, sfs sysfs.FS, onlineCPUs cpuset.CPUSet) error {
	var err error
	prm.pcieDomains, err = device.PCIeDomainsFromFS(logger, sfs)
	if err != nil {
		return fmt.Errorf("failed to list PCIe domains: %w", err)
	}
	if len(prm.pcieDomains) > 0 {
		logger.V(2).Info("PCIe domains detected", "count", len(prm.pcieDomains))
		for idx, dom := range prm.pcieDomains {
			logger.V(4).Info("PCIe domain", "index", idx, "domain", dom.String())
		}
	} else {
		logger.Info("PCIe domains: none detected, device attributes will not be available")
	}
	extraCPUs := device.FindOrphanedCPUs(prm.pcieDomains, onlineCPUs)
	if !extraCPUs.IsEmpty() {
		// not critical, intentionally continue
		logger.Info("PCIe domains: detected cpus not local to any detected PCIe Root", "CPUs", extraCPUs.String())
	}

	prm.cpuIDToPCIeDomain = device.MapCPUsToPCIeDomain(prm.pcieDomains, onlineCPUs)
	logger.V(4).Info("mapped CPUs to PCIe domains", "count", len(prm.cpuIDToPCIeDomain))

	return nil
}

func (prm *PCIeRootMapper) GetPCIeRootsForCPU(cpuIDs ...int) []string {
	if len(prm.pcieDomains) == 0 {
		return nil
	}
	pcieRoots := sets.New[string]()
	for _, cpuID := range cpuIDs {
		for _, dom := range prm.cpuIDToPCIeDomain[cpuID] {
			pcieRoots.Insert(dom.RootName)
		}
	}
	return sets.List(pcieRoots)
}
