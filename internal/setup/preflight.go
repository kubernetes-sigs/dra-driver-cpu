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

// Adapted from dra-driver-memory d6a6c17, pkg/sysinfo/preflight.go.
package setup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/go-logr/logr"
	"github.com/moby/sys/mountinfo"
)

const (
	cgroup2FSType = "cgroup2"
)

var (
	ErrCGroupV2Missing         = errors.New("cgroup v2 not configured")
	ErrCGroupV2Repeated        = errors.New("cgroup v2 configured multiple times")
	ErrMemoryHugeTLBAccounting = errors.New("memory hugetlb accounting not supported")
)

func ValidateCgroups(lh logr.Logger, procRoot string) error {
	mounts, err := getThreadSelfMounts(procRoot, mountinfo.FSTypeFilter(cgroup2FSType))
	if err != nil {
		return fmt.Errorf("read cgroup mounts; expose procfs in the setup environment: %w", err)
	}
	if len(mounts) == 0 {
		return ErrCGroupV2Missing
	}
	if len(mounts) > 1 {
		return ErrCGroupV2Repeated
	}
	lh.V(2).Info("system check", "cgroupV2", "pass")
	mount := mounts[0] // shortcut
	lh.Info("cgroup2 mount", "options", mount.Options)
	if strings.Contains(mount.Options+","+mount.VFSOptions, "memory_hugetlb_accounting") {
		return ErrMemoryHugeTLBAccounting
	}
	lh.V(2).Info("system check", "memoryHugetlbSplitAccounting", "pass")
	return nil
}

// os thread locking inspired by moby/sys code
func getThreadSelfMounts(procRoot string, filter mountinfo.FilterFunc) (mounts []*mountinfo.Info, err error) {
	// We need to lock ourselves to the current OS thread in order to make sure
	// that the thread referenced by /proc/thread-self stays alive until we
	// finish parsing the file.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	src, err := os.Open(filepath.Join(procRoot, "proc", "thread-self", "mountinfo"))
	if err != nil {
		return nil, err
	}
	//nolint:errcheck
	defer func() { err = errors.Join(err, src.Close()) }()
	return mountinfo.GetMountsFromReader(src, filter)
}
