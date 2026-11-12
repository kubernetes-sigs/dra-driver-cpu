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

package cpupartition

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strconv"
	"strings"
)

// Before 7.0 a same-parent sibling writing an overlapping cpuset.cpus
// invalidates a partition, so a pod's other containers can undo it until the
// repair loop notices. Start strict and relax once older kernels are qualified.
const minKernelMajor, minKernelMinor = 7, 0

// osRelease returns the running kernel release. Tests replace it.
var osRelease = func() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/osrelease")
	return strings.TrimSpace(string(b)), err
}

// CheckHost refuses hosts where partitions cannot be kept valid: kernels older
// than 7.0, and hierarchies where the scope or one of its ancestors is already
// a partition. The Manager only makes container cgroups partitions, so such a
// cgroup has another writer, typically systemd CPUSetPartition=.
func (m *Manager) CheckHost() error {
	release, err := osRelease()
	if err != nil {
		return fmt.Errorf("read kernel release: %w", err)
	}
	major, minor, err := kernelVersion(release)
	if err != nil {
		return err
	}
	if major < minKernelMajor || (major == minKernelMajor && minor < minKernelMinor) {
		return fmt.Errorf("cpuset partitions need Linux %d.%d or later, running %s: upgrade the kernel or remove experimentalCPUSetPartition",
			minKernelMajor, minKernelMinor, release)
	}
	return m.withRoot(func(root *os.Root) error {
		for _, dir := range chain(m.scope) {
			mode, err := readFile(root, path.Join(dir, filePartition))
			if errors.Is(err, fs.ErrNotExist) {
				continue // no cpuset controller, or the scope does not exist yet
			}
			if err != nil {
				return fmt.Errorf("read %s partition: %w", dir, err)
			}
			if mode != modeMember {
				return fmt.Errorf("cgroup %s is already a %q cpuset partition, likely from systemd CPUSetPartition=: remove that setting or remove experimentalCPUSetPartition",
					dir, mode)
			}
		}
		return nil
	})
}

// kernelVersion parses the major and minor version of a release such as
// "6.19.10-300.fc44.aarch64" or "7.4.0-rc0".
func kernelVersion(release string) (int, int, error) {
	parts := strings.SplitN(release, ".", 3)
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("parse kernel release %q: want major.minor", release)
	}
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, fmt.Errorf("parse kernel release %q: %w", release, err)
	}
	minorDigits, _, _ := strings.Cut(parts[1], "-") // "0-rc1" in "7.0-rc1"
	minor, err := strconv.Atoi(minorDigits)
	if err != nil {
		return 0, 0, fmt.Errorf("parse kernel release %q: %w", release, err)
	}
	return major, minor, nil
}
