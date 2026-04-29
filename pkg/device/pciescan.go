/*
Copyright 2026 The Kubernetes Authors.

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

	"github.com/kubernetes-sigs/dra-driver-cpu/internal/deviceattribute"
	"k8s.io/klog/v2"
	"k8s.io/utils/cpuset"
)

const (
	SysfsRoot = "/sys"
)

var (
	pciAddrRegex *regexp.Regexp
)

func init() {
	// same regex as k8s' deviceattribute package, borrowed here
	pciAddrRegex = regexp.MustCompile(`^([0-9a-f]{4}):([0-9a-f]{2}):([0-9a-f]{2})\.([0-9a-f]{1})$`)
}

func OnlineCPUs(sysfs fs.ReadLinkFS) (cpuset.CPUSet, error) {
	cpuData, err := fs.ReadFile(sysfs, filepath.Join("devices", "system", "cpu", "online"))
	if err != nil {
		return cpuset.New(), err
	}
	allCPUs, err := cpuset.Parse(strings.TrimSpace(string(cpuData)))
	if err != nil {
		return cpuset.New(), err
	}
	klog.V(2).Infof("System: online CPUs %v", allCPUs.String())
	return allCPUs, nil
}

func FindOrphanedCPUs(domains []PCIEDomain, allCPUs cpuset.CPUSet) cpuset.CPUSet {
	orphanedCPUs := allCPUs.Clone()
	for _, dom := range domains {
		orphanedCPUs = orphanedCPUs.Difference(dom.LocalCPUs)
	}
	return orphanedCPUs
}

type PCIEDomain struct {
	PCIERootAttr deviceattribute.DeviceAttribute
	LocalCPUs    cpuset.CPUSet
	NUMANode     int
}

func (pcd PCIEDomain) Root() string {
	// if nil, it's a bug in GetPCIeRootAttributeByPCIBusID. We can't recover, so let's rather panic
	return *pcd.PCIERootAttr.Value.StringValue
}

func (pcd PCIEDomain) String() string {
	return fmt.Sprintf("<PCIERoot=%s CPUs=%s NUMANode=%d>", pcd.Root(), pcd.LocalCPUs.String(), pcd.NUMANode)
}

type PCIEDevice struct {
	Address    string
	ClassID    string
	SubclassID string
}

func (pciDev PCIEDevice) BaseDirPath() string {
	return filepath.Join("bus", "pci", "devices")
}

func (pciDev PCIEDevice) SysfsPath() string {
	return filepath.Join(pciDev.BaseDirPath(), pciDev.Address)
}

type SysFS interface {
	fs.ReadLinkFS
	fs.ReadDirFS
}

func ScanPCIDevices(sysfs SysFS, processDevice func(PCIEDevice) error) error {
	pciDevicesDir := filepath.Join("bus", "pci", "devices")
	entries, err := fs.ReadDir(sysfs, pciDevicesDir)
	if err != nil {
		return err
	}
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

		pciDev := PCIEDevice{
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

func PCIEDomainsFromFS(sysfs SysFS) ([]PCIEDomain, error) {
	// we use a map (not a slice) to deduplicate PCIERoot domains.
	// During the PCIE device iteration, we should ideally perform a precise tracking
	// and find the PCIE root downstream ports for each domain.
	// Instead, we take a simpler approach and look for the PCI-to-PCI bridges.
	// The roots are, per PCIe docs, a subset of the bridges we identified.
	// We start with a simplified scan, depending on the fact that the linux kernel is
	// expected to report the same local cpulist for all the devices belonging to the
	// same root complex.
	// But the, we can turn out with bogus duplicate domains, so we need a final
	// deduplication step.
	domains := make(map[string]PCIEDomain)

	err := ScanPCIDevices(sysfs, func(pciDev PCIEDevice) error {
		if !isPCIBridge(pciDev) {
			return nil
		}

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

		pcd := PCIEDomain{
			PCIERootAttr: pcieRootAttr,
			LocalCPUs:    localCPUs,
			NUMANode:     numaNode,
		}
		domains[pcd.Root()] = pcd
		klog.V(2).Infof("PCIE: bridge at %v: %v", pciDev.Address, pcd.String())

		return nil
	})

	if err != nil {
		return nil, err
	}
	doms := slices.Collect(maps.Values(domains))
	slices.SortFunc(doms, func(a, b PCIEDomain) int {
		return strings.Compare(a.Root(), b.Root())
	})
	return doms, nil
}

// isPCIBridge checks if a device is a PCI-to-PCI bridge. The PCIE Roots are a subset of these.
// We check this class of devices because these are always present in the systems, while we can't
// predict which class of devices users will be available, or user interested to.
// Reference: https://pci-ids.ucw.cz/
func isPCIBridge(dev PCIEDevice) bool {
	// class 06: Bridge
	// subclass 04: PCI bridge
	// TODO: what about subclasses 09 (semi-transparent PCI bridge) and 0a (Infiniband to PCI)?
	return dev.ClassID == "06" && dev.SubclassID == "04"
}

// isValidPCIAddress checks if s matches the format
// DDDD:BB:SS.F (domain:bus:slot.function)
// where each letter is a hex digit.
func isValidPCIAddress(addr string) bool {
	return pciAddrRegex.MatchString(addr)
}
