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
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/sysfs"
	"k8s.io/utils/cpuset"
)

func PCIeDomainsFromFS(logger logr.Logger, sfs sysfs.FS) ([]PCIeDomain, error) {
	logger.V(6).Info("begin: processing PCIe domains")
	defer logger.V(6).Info("end: processing PCIe domains")

	entries, err := fs.ReadDir(sfs, "devices")
	if err != nil {
		return nil, err
	}

	roots := []PCIeDomain{}
	for _, entry := range entries {
		entryName := entry.Name()
		if !IsPCIeRootName(entryName) {
			continue
		}

		busID := strings.TrimPrefix(entryName, "pci")

		busAffinityData, err := fs.ReadFile(sfs, filepath.Join("devices", entryName, "pci_bus", busID, "cpulistaffinity"))
		if err != nil {
			return nil, err
		}

		busAffinity, err := cpuset.Parse(strings.TrimSpace(string(busAffinityData)))
		if err != nil {
			return nil, err
		}

		dom := PCIeDomain{
			RootName:  entryName,
			LocalCPUs: busAffinity,
		}
		roots = append(roots, dom)

		logger.V(6).Info("found PCIe root", "domain", dom.String())
	}

	return roots, nil
}
