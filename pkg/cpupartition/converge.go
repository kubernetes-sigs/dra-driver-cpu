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
	"slices"
	"strings"

	"k8s.io/utils/cpuset"
)

const (
	fileCPUs            = "cpuset.cpus"
	fileEffective       = "cpuset.cpus.effective"
	fileExclusive       = "cpuset.cpus.exclusive"
	fileExclusiveEffect = "cpuset.cpus.exclusive.effective"
	filePartition       = "cpuset.cpus.partition"
	fileIsolated        = "cpuset.cpus.isolated" // root cgroup only
	modeMember          = "member"
)

// dirState is the partition state of one cgroup.
type dirState struct {
	cpuset    bool // the cgroup exists with the cpuset controller
	exclusive cpuset.CPUSet
	mode      string // as read, e.g. "root invalid (...)"
}

// desired is what the leases need from one cgroup. mode and leaf are only set
// for the cgroup of a lease.
type desired struct {
	exclusive cpuset.CPUSet
	mode      string
	leaf      *lease
}

// writeFile writes an existing cgroup file; cgroup lifetimes belong to the
// runtime. Tests replace it to inject kernel errors.
var writeFile = func(root *os.Root, name, value string) error {
	f, err := root.OpenFile(name, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	// Without the newline an empty value is not written at all.
	_, err = f.WriteString(value + "\n")
	return errors.Join(err, f.Close())
}

func readFile(root *os.Root, name string) (string, error) {
	b, err := root.ReadFile(name)
	return strings.TrimSpace(string(b)), err
}

func readCPUs(root *os.Root, name string) (cpuset.CPUSet, error) {
	v, err := readFile(root, name)
	if err != nil {
		return cpuset.New(), err
	}
	cpus, err := cpuset.Parse(v)
	if err != nil {
		return cpuset.New(), fmt.Errorf("parse %s: %w", name, err)
	}
	return cpus, nil
}

// readDir reads a cgroup. A missing cgroup, or one without the cpuset
// controller, holds no partition state.
func readDir(root *os.Root, dir string) (dirState, error) {
	exclusive, err := readCPUs(root, path.Join(dir, fileExclusive))
	if errors.Is(err, fs.ErrNotExist) {
		return dirState{}, nil
	}
	if err != nil {
		return dirState{}, err
	}
	mode, err := readFile(root, path.Join(dir, filePartition))
	if err != nil {
		return dirState{}, err
	}
	return dirState{cpuset: true, exclusive: exclusive, mode: mode}, nil
}

// scan returns the cgroups in scope that hold partition state. Below a cgroup
// without the cpuset controller no cgroup can have it.
func scan(root *os.Root, scope string) ([]string, error) {
	var found []string
	err := fs.WalkDir(root.FS(), scope, func(dir string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return err
		}
		st, err := readDir(root, dir)
		switch {
		case err != nil:
			return err
		case !st.cpuset:
			return fs.SkipDir
		case !st.exclusive.IsEmpty() || st.mode != modeMember:
			found = append(found, dir)
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return found, err
}

type step func(dir string, st dirState, w desired) (dirState, error)

// converge moves dirs to the state want returns for them. Writes go in an
// order the kernel accepts and are skipped where the kernel already agrees,
// so converge can be repeated freely. In strict mode it stops at the first
// error; otherwise it tries every cgroup and returns all errors.
func converge(root *os.Root, dirs []string, want func(string) desired, strict bool) error {
	dirs = slices.Clone(dirs)
	slices.Sort(dirs)
	dirs = slices.Compact(dirs)
	bottomUp := slices.SortedStableFunc(slices.Values(dirs), func(a, b string) int {
		return strings.Count(b, "/") - strings.Count(a, "/")
	})
	topDown := slices.Clone(bottomUp)
	slices.Reverse(topDown)

	c := cgroupfs{root}
	steps := []struct {
		order []string
		do    step
	}{
		{bottomUp, c.load},
		// A partition must become a member before its grant can shrink.
		{bottomUp, c.demote},
		{bottomUp, c.shrink},
		// A child's grant must stay a subset of its parent's.
		{topDown, c.grow},
		{bottomUp, c.activate},
	}
	states := map[string]dirState{}
	var errs []error
	for _, s := range steps {
		for _, dir := range s.order {
			st, err := s.do(dir, states[dir], want(dir))
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", dir, err))
				if strict {
					return errors.Join(errs...)
				}
				continue
			}
			states[dir] = st
		}
	}
	return errors.Join(errs...)
}

type cgroupfs struct{ root *os.Root }

func (c cgroupfs) write(dir, name, value string) error {
	if err := writeFile(c.root, path.Join(dir, name), value); err != nil {
		return fmt.Errorf("write %s=%q: %w", name, value, err)
	}
	return nil
}

func (c cgroupfs) load(dir string, _ dirState, w desired) (dirState, error) {
	st, err := readDir(c.root, dir)
	if err == nil && !st.cpuset && (!w.exclusive.IsEmpty() || w.leaf != nil) {
		err = errors.New("cgroup or its cpuset controller is missing")
	}
	return st, err
}

// demote makes a partition a member unless it is, or is an invalid instance
// of, the wanted type; an invalid one is re-activated in place.
func (c cgroupfs) demote(dir string, st dirState, w desired) (dirState, error) {
	if !st.cpuset || st.mode == modeMember || st.mode == w.mode || (w.mode != "" && strings.HasPrefix(st.mode, w.mode+" invalid")) {
		return st, nil
	}
	st.mode = modeMember
	return st, c.write(dir, filePartition, modeMember)
}

func (c cgroupfs) shrink(dir string, st dirState, w desired) (dirState, error) {
	next := st.exclusive.Intersection(w.exclusive)
	if !st.cpuset || next.Equals(st.exclusive) {
		return st, nil
	}
	st.exclusive = next
	return st, c.write(dir, fileExclusive, next.String())
}

func (c cgroupfs) grow(dir string, st dirState, w desired) (dirState, error) {
	if !st.cpuset || st.exclusive.Equals(w.exclusive) {
		return st, nil
	}
	st.exclusive = w.exclusive
	return st, c.write(dir, fileExclusive, w.exclusive.String())
}

// activate refuses a cgroup whose runtime mask is not the one CreateContainer
// requested: it may not belong to the container we were told about.
func (c cgroupfs) activate(dir string, st dirState, w desired) (dirState, error) {
	if !st.cpuset || w.leaf == nil || st.mode == w.mode {
		return st, nil
	}
	cpus, err := readCPUs(c.root, path.Join(dir, fileCPUs))
	if err != nil {
		return st, err
	}
	if !cpus.Equals(w.leaf.cpus) {
		return st, fmt.Errorf("runtime cpuset is %q, want %q", cpus.String(), w.leaf.cpus.String())
	}
	st.mode = w.mode
	return st, c.write(dir, filePartition, w.mode)
}

// verify checks what the kernel made of a lease. A successful write proves
// nothing: an accepted partition can read "root invalid (...)".
func verify(root *os.Root, l *lease, mode string) error {
	got, err := readFile(root, path.Join(l.path, filePartition))
	if err != nil {
		return err
	}
	if got != mode {
		return fmt.Errorf("partition %s is %q, want %q", l.path, got, mode)
	}
	for _, name := range []string{fileExclusiveEffect, fileEffective} {
		cpus, err := readCPUs(root, path.Join(l.path, name))
		if err != nil {
			return err
		}
		if !cpus.Equals(l.cpus) {
			return fmt.Errorf("%s of %s is %q, want %q", name, l.path, cpus.String(), l.cpus.String())
		}
	}
	if mode != ModeIsolated {
		return nil
	}
	isolated, err := readCPUs(root, fileIsolated)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err == nil && !l.cpus.IsSubsetOf(isolated) {
		err = fmt.Errorf("root %s is %q, missing %q", fileIsolated, isolated.String(), l.cpus.String())
	}
	return err
}
