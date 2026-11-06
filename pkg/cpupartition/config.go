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

// Package cpupartition makes the cgroups of containers with exclusive CPUs
// cgroup v2 remote cpuset partitions (Linux 6.7 or later).
//
// The Manager writes cpuset.cpus.exclusive and cpuset.cpus.partition directly.
// It must be their only writer under Scope and its ancestors, so kubelet's
// cpuManagerPolicy must be none and no systemd unit there may set
// CPUSetPartition=. The kernel state follows from the running containers
// alone, so a restarted driver reconciles it instead of reading a checkpoint.
package cpupartition

import (
	"fmt"
	"path/filepath"
)

const (
	ModeRoot     = "root"
	ModeIsolated = "isolated"

	DefaultCgroupRoot = "/sys/fs/cgroup"
	DefaultScope      = "kubepods.slice"
)

// Config enables partitions when Mode is set.
type Config struct {
	// Mode is the partition type: root, or isolated to also disable load
	// balancing and unbound workqueues on the CPUs.
	Mode string `json:"mode,omitempty"`
	// CgroupRoot is the host cgroup v2 mount, writable by the driver.
	CgroupRoot string `json:"cgroupRoot,omitempty"`
	// Scope is the subtree of CgroupRoot holding container cgroups. Partition
	// state in it that no running container explains is removed.
	Scope string `json:"scope,omitempty"`
}

func (c Config) Enabled() bool {
	return c.Mode != ""
}

func (c Config) Validate() error {
	if !c.Enabled() {
		return nil
	}
	if c.Mode != ModeRoot && c.Mode != ModeIsolated {
		return fmt.Errorf("invalid mode %q: must be %q or %q", c.Mode, ModeRoot, ModeIsolated)
	}
	if !filepath.IsAbs(c.CgroupRoot) {
		return fmt.Errorf("invalid cgroupRoot %q: must be an absolute path", c.CgroupRoot)
	}
	// The whole hierarchy is never a valid scope: system.slice is in it.
	if c.Scope == "." || !filepath.IsLocal(c.Scope) || filepath.Clean(c.Scope) != c.Scope {
		return fmt.Errorf("invalid scope %q: must be a clean subdirectory of cgroupRoot", c.Scope)
	}
	return nil
}
