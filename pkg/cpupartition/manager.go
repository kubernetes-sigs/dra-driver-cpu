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
	"context"
	"errors"
	"fmt"
	"iter"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/fsnotify/fsnotify"
	"k8s.io/utils/cpuset"
)

// Lease is a container's exclusive CPUs and its OCI cgroupsPath.
type Lease struct {
	CgroupsPath string
	CPUs        cpuset.CPUSet
}

type lease struct {
	path string // relative to the cgroup root
	cpus cpuset.CPUSet
}

// pending is a released lease whose cgroups are not clean yet. It no longer
// shapes the desired state but still holds its CPUs.
type pending struct {
	id string
	*lease
}

// Status counts partitions after Reconcile or Repair.
type Status struct {
	Valid, Invalid, Releasing int
	// Repaired counts partitions Repair found invalid and restored.
	Repaired int
	// Errors holds the error of each invalid or releasing container.
	Errors map[string]error
	// Cleanup holds errors removing state no container explains.
	Cleanup error
}

// Manager is not safe for concurrent use; the driver calls it under its lock.
type Manager struct {
	cgroupRoot, scope, mode string
	leases                  map[string]*lease
	pending                 []pending
	watcher                 *fsnotify.Watcher
	watchDone               chan struct{}
}

func New(cfg Config) (*Manager, error) {
	if !cfg.Enabled() {
		return nil, errors.New("cpuset partitions are disabled")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	return &Manager{cgroupRoot: cfg.CgroupRoot, scope: cfg.Scope, mode: cfg.Mode, leases: map[string]*lease{}, watcher: w}, nil
}

// Close stops Watch. Partitions are left in place for the next driver.
func (m *Manager) Close() error {
	err := m.watcher.Close()
	if m.watchDone != nil {
		<-m.watchDone
	}
	return err
}

// Watch signals, coalesced, when a partition file changes and every interval;
// the receiver should call Repair. The channel closes when ctx ends or Close
// is called. The kernel signals a partition whenever its validity changes.
func (m *Manager) Watch(ctx context.Context, interval time.Duration) <-chan struct{} {
	out := make(chan struct{}, 1)
	m.watchDone = make(chan struct{})
	go func() {
		defer close(m.watchDone)
		defer close(out)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-m.watcher.Events:
				if !ok {
					return
				}
			case _, ok := <-m.watcher.Errors: // e.g. queue overflow: verify all
				if !ok {
					return
				}
			case <-ticker.C:
			}
			select {
			case out <- struct{}{}:
			default:
			}
		}
	}()
	return out
}

func (m *Manager) watchFile(l *lease) string {
	return filepath.Join(m.cgroupRoot, l.path, filePartition)
}

func (m *Manager) withRoot(fn func(*os.Root) error) error {
	root, err := os.OpenRoot(m.cgroupRoot)
	if err != nil {
		return err
	}
	defer root.Close()
	return fn(root)
}

func (m *Manager) leafPath(cgroupsPath string) (string, error) {
	p, err := CgroupPath(cgroupsPath)
	if err != nil {
		return "", err
	}
	if p == m.scope || !within(p, m.scope) {
		return "", fmt.Errorf("cgroup %s is outside scope %s", p, m.scope)
	}
	return p, nil
}

// held yields the leases that may own CPUs in the kernel: active ones and
// pending releases.
func (m *Manager) held() iter.Seq2[string, *lease] {
	return func(yield func(string, *lease) bool) {
		for _, id := range slices.Sorted(maps.Keys(m.leases)) {
			if !yield(id, m.leases[id]) {
				return
			}
		}
		for _, r := range m.pending {
			if !yield(r.id, r.lease) {
				return
			}
		}
	}
}

func (m *Manager) conflict(p string, cpus cpuset.CPUSet) error {
	for id, l := range m.held() {
		if !l.cpus.Intersection(cpus).IsEmpty() {
			return fmt.Errorf("CPUs %s overlap the partition of container %s", cpus.String(), id)
		}
		if within(p, l.path) || within(l.path, p) {
			return fmt.Errorf("cgroup %s nests with the partition of container %s", p, id)
		}
	}
	return nil
}

// Holding returns the containers whose partitions hold any of cpus. The
// allocator must not reuse those CPUs yet.
func (m *Manager) Holding(cpus cpuset.CPUSet) []string {
	var ids []string
	for id, l := range m.held() {
		if !l.cpus.Intersection(cpus).IsEmpty() && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids
}

func (m *Manager) want(dir string) desired {
	d := desired{exclusive: cpuset.New()}
	for _, l := range m.leases {
		if !within(l.path, dir) {
			continue
		}
		d.exclusive = d.exclusive.Union(l.cpus)
		if l.path == dir {
			d.mode, d.leaf = m.mode, l
		}
	}
	return d
}

// Activate grants cpus down the container's cgroup chain, makes its cgroup a
// partition and verifies it. It rolls back on failure, so the caller can
// refuse to start the container. Repeating it only verifies.
func (m *Manager) Activate(id, cgroupsPath string, cpus cpuset.CPUSet) error {
	if id == "" || cpus.IsEmpty() {
		return errors.New("partition requires a container ID and nonempty CPUs")
	}
	p, err := m.leafPath(cgroupsPath)
	if err != nil {
		return err
	}
	return m.withRoot(func(root *os.Root) error {
		if l := m.leases[id]; l != nil {
			if l.path != p || !l.cpus.Equals(cpus) {
				return fmt.Errorf("container %s already holds a different partition", id)
			}
			return verify(root, l, m.mode)
		}
		// A replacement container usually reuses the CPUs just released.
		m.settle(root, nil)
		if err := m.conflict(p, cpus); err != nil {
			return err
		}
		l := &lease{path: p, cpus: cpus}
		m.leases[id] = l
		err := converge(root, chain(p), m.want, true)
		if err == nil {
			err = verify(root, l, m.mode)
		}
		if err == nil {
			err = m.watcher.Add(m.watchFile(l))
		}
		if err != nil {
			m.drop(id)
			errs := m.settle(root, &id)
			return errors.Join(err, errs[id])
		}
		return nil
	})
}

// Release takes the container's CPUs out of its cgroup chain, leaving other
// containers' grants. An incomplete release keeps the CPUs and is retried.
// A missing cgroup is fine: the runtime may have removed it already.
func (m *Manager) Release(id string) error {
	if m.leases[id] == nil && !slices.ContainsFunc(m.pending, func(r pending) bool { return r.id == id }) {
		return nil
	}
	return m.withRoot(func(root *os.Root) error {
		if m.leases[id] != nil {
			m.drop(id)
		}
		return m.settle(root, &id)[id]
	})
}

func (m *Manager) drop(id string) {
	l := m.leases[id]
	m.pending = append(m.pending, pending{id: id, lease: l})
	delete(m.leases, id)
	_ = m.watcher.Remove(m.watchFile(l)) // gone with its cgroup
}

// settle retries the pending releases of only, or all if nil, and forgets
// those whose cgroups are clean.
func (m *Manager) settle(root *os.Root, only *string) map[string]error {
	errs := map[string]error{}
	m.pending = slices.DeleteFunc(m.pending, func(r pending) bool {
		if only != nil && r.id != *only {
			return false
		}
		err := converge(root, chain(r.path), m.want, false)
		if err != nil {
			errs[r.id] = errors.Join(errs[r.id], fmt.Errorf("release partition of container %s: %w", r.id, err))
		}
		return err == nil
	})
	return errs
}

// Reconcile makes the leases those of expected, the running containers with
// exclusive CPUs, and converges the whole scope to them. It adopts matching
// partitions, activates missing ones and removes grants nobody holds, such as
// those of a container deleted while the driver was down.
//
// It returns an error, without writing anything, only if the scope cannot be
// read. A container whose partition cannot be honoured is reported in Status
// and does not block the others.
func (m *Manager) Reconcile(expected map[string]Lease) (st Status, err error) {
	err = m.withRoot(func(root *os.Root) error {
		dirs, err := scan(root, m.scope)
		if err != nil {
			return fmt.Errorf("scan %s: %w", m.scope, err)
		}
		for _, id := range slices.Sorted(maps.Keys(m.leases)) {
			l, e := m.leases[id], expected[id]
			if p, err := m.leafPath(e.CgroupsPath); err != nil || p != l.path || !l.cpus.Equals(e.CPUs) {
				m.drop(id)
			}
		}
		rejected := map[string]error{}
		for _, id := range slices.Sorted(maps.Keys(expected)) {
			if m.leases[id] != nil {
				continue
			}
			e := expected[id]
			p, err := m.leafPath(e.CgroupsPath)
			if err == nil && (id == "" || e.CPUs.IsEmpty()) {
				err = errors.New("partition requires a container ID and nonempty CPUs")
			}
			if err == nil {
				err = m.conflict(p, e.CPUs)
			}
			if err != nil {
				rejected[id] = err
				continue
			}
			m.leases[id] = &lease{path: p, cpus: e.CPUs}
		}
		dirs = append(dirs, chain(m.scope)...)
		for _, l := range m.held() {
			dirs = append(dirs, chain(l.path)...)
		}
		cleanup := converge(root, dirs, m.want, false)
		st = m.status(root, m.settle(root, nil))
		st.Cleanup = cleanup
		for id, err := range rejected {
			st.Invalid++
			st.Errors[id] = err
		}
		return nil
	})
	return st, err
}

// Repair restores partitions the kernel invalidated, for example when an
// ancestor grant was withdrawn: the kernel does not revalidate a partition
// when the grant comes back. It also retries pending releases. A partition
// stays invalid while its cause persists, such as an offline CPU.
func (m *Manager) Repair() (st Status, err error) {
	err = m.withRoot(func(root *os.Root) error {
		releaseErrs := m.settle(root, nil)
		repaired := 0
		for _, id := range slices.Sorted(maps.Keys(m.leases)) {
			l := m.leases[id]
			if verify(root, l, m.mode) == nil {
				continue
			}
			if converge(root, chain(l.path), m.want, false) == nil && verify(root, l, m.mode) == nil {
				repaired++
			}
		}
		st = m.status(root, releaseErrs)
		st.Repaired = repaired
		return nil
	})
	return st, err
}

func (m *Manager) status(root *os.Root, releaseErrs map[string]error) Status {
	st := Status{Errors: releaseErrs, Releasing: len(m.pending)}
	for id, l := range m.leases {
		err := verify(root, l, m.mode)
		if err == nil {
			err = m.watcher.Add(m.watchFile(l)) // re-arms a watch lost to its cgroup
		}
		if err != nil {
			st.Invalid++
			st.Errors[id] = err
			continue
		}
		st.Valid++
	}
	return st
}
