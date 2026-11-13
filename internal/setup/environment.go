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

package setup

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
)

func CheckEnvironment(logger logr.Logger, root string, memory, kindNode bool) error {
	if err := ValidateCgroups(logger, root); err != nil {
		return fmt.Errorf("use unified cgroup v2 without memory_hugetlb_accounting: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(root, "sys/fs/cgroup/cgroup.controllers"))
	if err != nil {
		return fmt.Errorf("read delegated controllers; expose cgroup v2: %w", err)
	}
	controllers := " " + strings.Join(strings.Fields(string(data)), " ") + " "
	for _, name := range []string{"cpuset", "memory", "hugetlb"} {
		if !strings.Contains(controllers, " "+name+" ") {
			return fmt.Errorf("cgroup controller %s is missing; delegate it before creating kind nodes", name)
		}
	}
	swaps, err := os.ReadFile(filepath.Join(root, "proc/swaps"))
	if err != nil {
		return fmt.Errorf("read swap inventory before setup: %w", err)
	}
	if len(strings.Split(strings.TrimSpace(string(swaps)), "\n")) > 1 {
		return fmt.Errorf("swap is active; disable it on the disposable host before setup")
	}
	if kindNode {
		for file, minimum := range map[string]int64{"hugetlb.2MB.max": 2 << 20} {
			data, err := os.ReadFile(filepath.Join(root, "sys/fs/cgroup", file))
			if err != nil {
				return fmt.Errorf("read kind-node parent %s; enable/delegate hugetlb: %w", file, err)
			}
			if err := CheckParentBudget(string(data), minimum, file); err != nil {
				return err
			}
		}
	}
	if memory && !kindNode {
		nodes, err := filepath.Glob(filepath.Join(root, "sys/devices/system/node/node[0-9]*"))
		if err != nil {
			return fmt.Errorf("enumerate NUMA nodes: %w", err)
		}
		found := false
		for _, node := range nodes {
			ready := true
			for _, size := range []string{"2048"} {
				data, err := os.ReadFile(filepath.Join(node, "hugepages", "hugepages-"+size+"kB", "nr_hugepages"))
				if os.IsNotExist(err) {
					ready = false
					continue
				}
				if err != nil {
					return fmt.Errorf("read hugepage inventory: %w", err)
				}
				count, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
				if err != nil {
					return fmt.Errorf("parse hugepage count: %w", err)
				}
				if count <= 0 {
					ready = false
				}
			}
			found = found || ready
		}
		if !found {
			return fmt.Errorf("provision 2 MiB hugepages on a NUMA node before setup; follow docs/user/memory-poc.md")
		}
	}
	return nil
}

func CheckParentBudget(data string, minimum int64, name string) error {
	text := strings.TrimSpace(data)
	if text == "max" {
		return nil
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return fmt.Errorf("parse parent %s: %w", name, err)
	}
	if value < minimum {
		return fmt.Errorf("parent %s=%d cannot admit %d bytes; remove the kind node's restrictive parent limit", name, value, minimum)
	}
	return nil
}

func CheckCRI(src io.Reader) error {
	var info struct {
		Status struct {
			Conditions []struct {
				Type   string `json:"type"`
				Status bool   `json:"status"`
			} `json:"conditions"`
		} `json:"status"`
	}
	if err := json.NewDecoder(src).Decode(&info); err != nil {
		return fmt.Errorf("decode crictl info: %w", err)
	}
	for _, condition := range info.Status.Conditions {
		if condition.Type == "RuntimeReady" && condition.Status {
			return nil
		}
	}
	return fmt.Errorf("containerd CRI is not ready; inspect its startup journal before deploying the driver")
}
