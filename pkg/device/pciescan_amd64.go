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
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/utils/cpuset"
)

var (
	pciAddrRegex *regexp.Regexp
)

func init() {
	// same regex as k8s' deviceattribute package, borrowed here
	pciAddrRegex = regexp.MustCompile(`^([0-9a-f]{4}):([0-9a-f]{2}):([0-9a-f]{2})\.([0-9a-f]{1})$`)
}

func ScanPCIeDevices(logger logr.Logger, sysfs SysFS, processDevice func(PCIeDevice) error) error {
	pciDevicesDir := filepath.Join("bus", "pci", "devices")
	entries, err := fs.ReadDir(sysfs, pciDevicesDir)
	if err != nil {
		return err
	}
	logger.V(6).Info("begin: processing PCIe devices", "devices", len(entries))
	defer logger.V(6).Info("end: processing PCIe devices", "devices", len(entries))

	for _, entry := range entries {
		pciAddress := entry.Name()
		if !isValidPCIAddress(pciAddress) {
			continue
		}

		classData, err := fs.ReadFile(sysfs, filepath.Join(pciDevicesDir, pciAddress, "class"))
		if err != nil {
			return err
		}

		pciClassInfo := strings.TrimSpace(string(classData))
		if len(pciClassInfo) != 8 { // format: "0xCCSSpp"
			return fmt.Errorf("invalid PCI Class data: %q", pciClassInfo)
		}

		pciDev := PCIeDevice{
			Address:    pciAddress,
			ClassID:    pciClassInfo[2:4],
			SubclassID: pciClassInfo[4:6],
		}

		err = processDevice(pciDev)
		if err != nil {
			return err
		}
	}
	return nil
}

func PCIeDomainsFromFS(logger logr.Logger, sysfs SysFS) ([]PCIeDomain, error) {
	// Scan all the PCIe devices, and for each of them we find its PCIe root.
	// PCIe devices sharing the same bus will share the same root, therefore
	// we need to deduplicate the roots.
	// Physically on the silicon, the roots are closer to a memory region (NUMA node)
	// and to a group of CPU cores (cpulist). Note that some layouts make all the
	// regions and all the cores equally distant, but that's a corner case.
	// The physical proximity of the root is a property inherited by all the buses
	// and all the devices on the buses. Therefore we can safely use the simple
	// latest-write-win approach.
	// Since we scan from the leaves of the PCIe trees, this approach can't miss
	// any device and any active root. The drawback is we process all the devices
	// on the system. A smarter and equally valid approach could be to enumerate
	// all the PCI buses.
	domains := make(map[string]PCIeDomain)

	err := ScanPCIeDevices(logger, sysfs, func(pciDev PCIeDevice) error {
		pcieRootAttr, err := deviceattribute.GetPCIeRootAttributeByPCIBusID(pciDev.Address, deviceattribute.WithFS(sysfs))
		if err != nil {
			return err
		}

		cpuData, err := fs.ReadFile(sysfs, filepath.Join(pciDev.SysfsPath(), "local_cpulist"))
		if err != nil {
			return err
		}
		localCPUs, err := cpuset.Parse(strings.TrimSpace(string(cpuData)))
		if err != nil {
			return err
		}

		numaData, err := fs.ReadFile(sysfs, filepath.Join(pciDev.SysfsPath(), "numa_node"))
		if err != nil {
			return err
		}
		numaNode, err := strconv.Atoi(strings.TrimSpace(string(numaData)))
		if err != nil {
			return err
		}

		pcd := PCIeDomain{
			PCIeRootAttr: pcieRootAttr,
			LocalCPUs:    localCPUs,
			NUMANode:     numaNode,
		}
		domains[pcd.Root()] = pcd
		logger.V(6).Info("PCIe device mapped to domain", "device", pciDev.String(), "domain", pcd.String())

		return nil
	})

	if err != nil {
		return nil, err
	}
	doms := slices.Collect(maps.Values(domains))
	slices.SortFunc(doms, func(a, b PCIeDomain) int {
		return strings.Compare(a.Root(), b.Root())
	})
	return doms, nil
}

// isValidPCIAddress checks if `addr` matches the format
// DDDD:BB:SS.F (domain:bus:slot.function)
// where each letter is a hex digit.
func isValidPCIAddress(addr string) bool {
	return pciAddrRegex.MatchString(addr)
}
