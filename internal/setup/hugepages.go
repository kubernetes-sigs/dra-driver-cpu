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

// Adapted from dra-driver-memory d6a6c17, pkg/hugepages/provision and its API.
package setup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	"k8s.io/utils/cpuset"
	"sigs.k8s.io/yaml"
)

func ReadConfiguration(source string) (config HugePageProvision, err error) {
	if source == "-" {
		return ReadConfigurationFrom(os.Stdin)
	}
	src, err := os.Open(source)
	if err != nil {
		return HugePageProvision{}, err
	}
	defer func() { err = errors.Join(err, src.Close()) }()
	return ReadConfigurationFrom(src)
}

func RuntimeHugepages(logger logr.Logger, hpp HugePageProvision, sysRoot string, numaZones int) error {
	logger.V(2).Info("start provisioning hugepages", "groups", len(hpp.Spec.Pages))
	defer logger.V(2).Info("done provisioning hugepages", "groups", len(hpp.Spec.Pages))

	for _, conf := range hpp.Spec.Pages {
		var err error
		// can't be lower on machines >= 2025
		if numaZones == 1 {
			numaNode := pickNode(conf)
			logger.V(0).Info("provisioning pages", "numaNode", numaNode, "count", conf.Count, "size", conf.Size)
			err = provisionOnNode(logger, numaNode, int(conf.Count), conf.Size, sysRoot)
		} else {
			logger.V(0).Info("splitting pages", "count", conf.Count, "NUMACount", numaZones)
			err = provisionOnMultiNode(logger, numaZones, int(conf.Count), conf.Size, sysRoot)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func provisionOnMultiNode(logger logr.Logger, numaNodeCount, hpCount int, hpSize string, sysRoot string) error {
	extra := hpCount % numaNodeCount
	perNode := hpCount / numaNodeCount

	// we choose to move excess pages on numa node 0 because this is the most common observed practice
	err := provisionOnNode(logger, 0, perNode+extra, hpSize, sysRoot)
	if err != nil {
		return err
	}
	for numaNode := 1; numaNode < numaNodeCount; numaNode++ {
		err = provisionOnNode(logger, numaNode, perNode, hpSize, sysRoot)
		if err != nil {
			return err
		}
	}
	return nil
}

func provisionOnNode(logger logr.Logger, numaNode, hpCount int, apiHpSize string, sysRoot string) (err error) {
	// this is done too late, we should have proper validation and API translation but good enough for starters.
	hpSize, err := validateHugePageSize(apiHpSize)
	if err != nil {
		return err
	}
	hpPath := filepath.Join(sysRoot, "sys", "devices", "system", "node", fmt.Sprintf("node%d", numaNode), "hugepages", "hugepages-"+hpSize, "nr_hugepages")
	logger.V(0).Info("writing on sysfs", "path", hpPath)
	dst, err := os.OpenFile(hpPath, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, dst.Close()) }()
	_, err = dst.WriteString(strconv.Itoa(hpCount))
	if err != nil {
		return fmt.Errorf("failed to write on %q: %w", hpPath, err)
	}
	observed, err := os.ReadFile(hpPath)
	if err != nil {
		return fmt.Errorf("read back %q after provisioning: %w", hpPath, err)
	}
	if strings.TrimSpace(string(observed)) != strconv.Itoa(hpCount) {
		return fmt.Errorf("kernel reserved %s pages at %s, requested %d; provision at boot or use an unfragmented host", strings.TrimSpace(string(observed)), hpPath, hpCount)
	}
	logger.V(0).Info("wrote on sysfs", "path", hpPath, "pages", hpCount)
	return err
}

func ReadConfigurationFrom(r io.Reader) (HugePageProvision, error) {
	hpp := HugePageProvision{}
	data, err := io.ReadAll(r)
	if err != nil {
		return hpp, err
	}
	err = yaml.UnmarshalStrict(data, &hpp)
	return hpp, err
}

func pickNode(hp HugePage) int {
	if hp.Node == nil {
		return 0
	}
	return int(*hp.Node)
}

func validateHugePageSize(size string) (string, error) {
	switch size {
	case "1G", "1Gi", "1g":
		return "1048576kB", nil
	case "2M", "2Mi", "2m":
		return "2048kB", nil
	default:
		return "", fmt.Errorf("unsupported hugepage size %q; use 1G or 2M on this amd64 POC", size)
	}
}

func ProvisionHugepages(logger logr.Logger, config HugePageProvision, root string) error {
	data, err := os.ReadFile(filepath.Join(root, "sys/devices/system/node/online"))
	if err != nil {
		return fmt.Errorf("read online NUMA nodes before provisioning: %w", err)
	}
	nodes, err := cpuset.Parse(strings.TrimSpace(string(data)))
	if err != nil {
		return fmt.Errorf("parse online NUMA nodes before provisioning: %w", err)
	}
	if nodes.IsEmpty() {
		return fmt.Errorf("no online NUMA nodes; expose sysfs before provisioning")
	}
	// The borrowed splitting algorithm assumes contiguous node IDs starting at zero.
	for i, node := range nodes.List() {
		if i != node {
			return fmt.Errorf("noncontiguous NUMA IDs %s; provision pages externally for this POC", nodes.String())
		}
	}
	if config.Kind != "HugePageProvision" || len(config.Spec.Pages) == 0 {
		return fmt.Errorf("use a HugePageProvision document with spec.pages")
	}
	for _, page := range config.Spec.Pages {
		if _, err := validateHugePageSize(page.Size); err != nil {
			return err
		}
		if page.Count < 0 {
			return fmt.Errorf("hugepage count must be nonnegative; correct the provisioning file")
		}
		if page.Node != nil && (!slices.Contains(nodes.List(), int(*page.Node)) || nodes.Size() > 1) {
			return fmt.Errorf("explicit node selection is supported only on a single-NUMA POC host; provision externally on this topology")
		}
	}
	return RuntimeHugepages(logger, config, root, nodes.Size())
}

type HugePageProvision struct {
	Kind     string         `json:"kind"`
	Metadata map[string]any `json:"metadata,omitempty"`
	Spec     struct {
		Pages []HugePage `json:"pages"`
	} `json:"spec"`
}
type HugePage struct {
	Size  string `json:"size"`
	Count int32  `json:"count"`
	Node  *int32 `json:"node,omitempty"`
}
