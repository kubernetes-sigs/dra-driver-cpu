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

package driver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/containerd/nri/pkg/api"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/cpuset"
)

// This POC assumes a disposable cgroup-v2 node, cpuManagerPolicy=none, and
// exclusive ownership of these files. stateMu serializes our hooks, not systemd
// or kubelet. No continuous hotplug repair or cold-start adoption is attempted.
// StartContainer is deliberately used instead of PostCreateContainer: containerd
// creates the task/cgroup before this callback, and starts it after it returns.
// Runtime configuration must treat this NRI plugin as required (fail closed).
// Checkpoint/restore paths that bypass StartContainer are outside this POC.
type cpusetPartitions struct {
	root, mode string
	nodes      map[string]*partitionNode
	leases     map[string]*partitionLease
}

type partitionNode struct {
	dir      *os.Root
	identity os.FileInfo
	owners   map[string]cpuset.CPUSet
	written  string
}

type partitionLease struct {
	path             string
	cpus             cpuset.CPUSet
	nodes            []string // ancestors first; release in reverse order
	activated, ready bool
}

func newCPUSetPartitions(root, mode string) (*cpusetPartitions, error) {
	if mode == "" {
		return nil, nil //nolint:nilnil // A nil controller means the experiment is disabled.
	}
	if mode != "root" && mode != "isolated" {
		return nil, fmt.Errorf("invalid experimental partition mode %q", mode)
	}
	if !filepath.IsAbs(root) {
		return nil, fmt.Errorf("experimental cgroup root must be absolute")
	}
	return &cpusetPartitions{root: root, mode: mode, nodes: map[string]*partitionNode{}, leases: map[string]*partitionLease{}}, nil
}

// partitionPath accepts OCI cgroupfs paths and systemd slice:prefix:id paths.
// Never clean away traversal or guess a path from a PID/name.
func partitionPath(raw string) (string, error) {
	if strings.Contains(raw, ":") {
		parts := strings.Split(raw, ":")
		if len(parts) != 3 || !strings.HasSuffix(parts[0], ".slice") || parts[0] == "-.slice" {
			return "", fmt.Errorf("unsupported systemd cgroup path %q", raw)
		}
		for _, part := range parts {
			if part == "" || strings.ContainsAny(part, "/\\\x00") || part == "." || part == ".." {
				return "", fmt.Errorf("invalid systemd cgroup path %q", raw)
			}
		}
		names := strings.Split(strings.TrimSuffix(parts[0], ".slice"), "-")
		path := ""
		for i, name := range names {
			if name == "" {
				return "", fmt.Errorf("invalid slice %q", parts[0])
			}
			path = filepath.Join(path, strings.Join(names[:i+1], "-")+".slice")
		}
		raw = filepath.Join(path, parts[1]+"-"+parts[2]+".scope")
	}
	for _, part := range strings.Split(raw, "/") {
		if part == ".." || part == "." {
			return "", fmt.Errorf("invalid cgroup path %q", raw)
		}
	}
	path := strings.TrimPrefix(raw, "/")
	if path == "" || !filepath.IsLocal(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("invalid cgroup path %q", raw)
	}
	return path, nil
}

func partitionRead(dir *os.Root, name string) (string, error) {
	b, err := dir.ReadFile(name)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

// Do not create files or directories. The runtime owns their lifetime.
func partitionWrite(dir *os.Root, name, value string) error {
	f, err := dir.OpenFile(name, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	_, err = f.WriteString(value + "\n")
	return errors.Join(err, f.Close())
}

// checkCPUs is shared by activation and reconnect verification. Ancestors may
// hold the union of several allocations; the container leaf must match exactly.
func (node *partitionNode) checkCPUs(file string, want cpuset.CPUSet, exact bool) error {
	raw, err := partitionRead(node.dir, file)
	if err != nil {
		return err
	}
	got, err := cpuset.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s at %s: %w", file, node.dir.Name(), err)
	}
	if !want.IsSubsetOf(got) || (exact && !want.Equals(got)) {
		return fmt.Errorf("%s at %s: got %q, want %s (exact=%t)", file, node.dir.Name(), raw, want.String(), exact)
	}
	return nil
}

func (node *partitionNode) checkMode(want string, allowInvalid bool) error {
	got, err := partitionRead(node.dir, "cpuset.cpus.partition")
	if err != nil {
		return err
	}
	if got != want && (!allowInvalid || !strings.HasPrefix(got, want+" invalid (")) {
		return fmt.Errorf("partition at %s: got %q, want %q", node.dir.Name(), got, want)
	}
	return nil
}

func (node *partitionNode) checkExclusive() error {
	actual, err := partitionRead(node.dir, "cpuset.cpus.exclusive")
	if err != nil {
		return err
	}
	if actual != node.written {
		return fmt.Errorf("foreign exclusive mask at %s: got %q, wrote %q", node.dir.Name(), actual, node.written)
	}
	return nil
}

// writeExclusive records a successful write before checking readback, so a
// failed verification leaves enough state for rollback or a later retry.
func (node *partitionNode) writeExclusive(exclude string) error {
	desired := cpuset.New()
	for id, cpus := range node.owners {
		if id != exclude {
			desired = desired.Union(cpus)
		}
	}
	if err := partitionWrite(node.dir, "cpuset.cpus.exclusive", desired.String()); err != nil {
		return err
	}
	node.written = desired.String()
	return node.checkExclusive()
}

func (p *cpusetPartitions) current(path string, node *partitionNode) (bool, error) {
	root, err := os.OpenRoot(p.root)
	if err != nil {
		return false, err
	}
	defer root.Close()
	info, err := root.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !os.SameFile(info, node.identity) {
		return false, fmt.Errorf("cgroup %s was replaced; refusing to release its ancestor entitlement", path)
	}
	return true, nil
}

func (p *cpusetPartitions) checkNode(path string, node *partitionNode) error {
	same, err := p.current(path, node)
	if err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("cgroup %s disappeared or was replaced", path)
	}
	return node.checkExclusive()
}

// openNode adopts only empty member state. A node is journaled by activate
// immediately after this returns; failed acquisition never leaks an open handle.
func (p *cpusetPartitions) openNode(root *os.Root, path string) (*partitionNode, error) {
	if node := p.nodes[path]; node != nil {
		if err := p.checkNode(path, node); err != nil {
			return nil, err
		}
		if err := node.checkMode("member", false); err != nil {
			return nil, err
		}
		return node, nil
	}
	dir, err := root.OpenRoot(path)
	if err != nil {
		return nil, err
	}
	node := &partitionNode{dir: dir, owners: map[string]cpuset.CPUSet{}}
	node.identity, err = dir.Stat(".")
	if err == nil {
		err = node.checkExclusive()
	}
	if err == nil {
		err = node.checkMode("member", false)
	}
	if err != nil {
		_ = dir.Close()
		return nil, err
	}
	return node, nil
}

func (p *cpusetPartitions) activate(ctx context.Context, id, raw string, cpus cpuset.CPUSet) (retErr error) {
	path, err := partitionPath(raw)
	if err != nil {
		return err
	}
	if id == "" || cpus.IsEmpty() {
		return fmt.Errorf("partition requires container ID and nonempty trusted CPUs")
	}
	if old := p.leases[id]; old != nil {
		if old.path != path || !old.cpus.Equals(cpus) || !old.ready {
			return fmt.Errorf("conflicting or incomplete partition for %s", id)
		}
		return p.verify(id)
	}
	for other, lease := range p.leases {
		if !cpus.Intersection(lease.cpus).IsEmpty() {
			return fmt.Errorf("partition overlaps container %s", other)
		}
	}
	root, err := os.OpenRoot(p.root)
	if err != nil {
		return err
	}
	defer root.Close()
	lease := &partitionLease{path: path, cpus: cpus}
	p.leases[id] = lease
	defer func() {
		if retErr != nil {
			retErr = errors.Join(retErr, p.release(id))
		}
	}()
	prefix := ""
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if err := ctx.Err(); err != nil {
			return err
		}
		prefix = filepath.Join(prefix, part)
		node, err := p.openNode(root, prefix)
		if err != nil {
			return err
		}
		p.nodes[prefix] = node
		node.owners[id] = cpus
		lease.nodes = append(lease.nodes, prefix)
		if err := node.writeExclusive(""); err != nil {
			return err
		}
		if err := node.checkCPUs("cpuset.cpus.exclusive.effective", cpus, false); err != nil {
			return err
		}
	}
	leaf := p.nodes[path]
	// NRI CreateContainer already requested the cpuset. Do not silently repair
	// a runtime that failed to apply it or chose a different cgroup.
	if err := leaf.checkCPUs("cpuset.cpus", cpus, true); err != nil {
		return err
	}
	if err := partitionWrite(leaf.dir, "cpuset.cpus.partition", p.mode); err != nil {
		return err
	}
	lease.activated = true
	if err := p.verify(id); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lease.ready = true
	return nil
}

func (p *cpusetPartitions) verify(id string) error {
	lease := p.leases[id]
	if lease == nil || !lease.activated || p.nodes[lease.path] == nil {
		return fmt.Errorf("container %s has no owned partition; cold adoption is unsupported", id)
	}
	for _, path := range lease.nodes {
		node := p.nodes[path]
		if err := p.checkNode(path, node); err != nil {
			return err
		}
		leaf := path == lease.path
		expected := "member"
		if leaf {
			expected = p.mode
		}
		if err := node.checkMode(expected, false); err != nil {
			return err
		}
		if err := node.checkCPUs("cpuset.cpus.exclusive.effective", lease.cpus, leaf); err != nil {
			return err
		}
	}
	return p.nodes[lease.path].checkCPUs("cpuset.cpus.effective", lease.cpus, true)
}

func (p *cpusetPartitions) release(id string) error {
	lease := p.leases[id]
	if lease == nil {
		return nil
	}
	// Keep the remaining journal on failure, allowing Stop/Remove/Synchronize to
	// retry. Never free a sibling's grant or touch a replacement cgroup inode.
	for len(lease.nodes) > 0 {
		path := lease.nodes[len(lease.nodes)-1]
		node := p.nodes[path]
		same, err := p.current(path, node)
		if err != nil {
			return err
		}
		if same {
			if err := node.checkExclusive(); err != nil {
				return err
			}
			if path == lease.path && lease.activated {
				if err := node.checkMode(p.mode, true); err != nil {
					return err
				}
				if err := partitionWrite(node.dir, "cpuset.cpus.partition", "member"); err != nil {
					return err
				}
				// The write succeeded. A readback failure must not leave us expecting the
				// old mode on retry, which would prevent cleanup of our own member state.
				lease.activated = false
			}
			if err := node.checkMode("member", false); err != nil {
				return err
			}
			if err := node.writeExclusive(id); err != nil {
				return err
			}
		}
		if path == lease.path {
			lease.activated = false
			lease.ready = false
		}
		delete(node.owners, id)
		if len(node.owners) == 0 {
			_ = node.dir.Close()
			delete(p.nodes, path)
		}
		lease.nodes = lease.nodes[:len(lease.nodes)-1]
	}
	delete(p.leases, id)
	return nil
}

func (p *cpusetPartitions) checkUnprepare(cpus cpuset.CPUSet) error {
	for id, lease := range p.leases {
		if !cpus.Intersection(lease.cpus).IsEmpty() {
			return fmt.Errorf("container %s still has partition state; Stop/Remove must release it first", id)
		}
	}
	return nil
}

func (cp *CPUDriver) StartContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) error {
	if cp.partitions == nil {
		return nil
	}
	cp.stateMu.Lock()
	defer cp.stateMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	state := cp.podConfigStore.GetContainerState(types.UID(pod.GetUid()), ctr.GetName())
	if state == nil || !state.MatchesContainer(ctr.GetName(), types.UID(ctr.GetId())) {
		return fmt.Errorf("partition start without matching CreateContainer state")
	}
	if !state.HasExclusiveCPUAllocation() {
		return nil
	}
	cpus := cpuset.New()
	for _, uid := range state.ClaimUIDs() {
		allocation, ok := cp.cpuAllocationStore.GetResourceClaimAllocation(uid)
		if !ok || !cp.claimTracker.IsOwner(uid, types.UID(pod.GetUid()), ctr.GetName()) {
			return fmt.Errorf("partition start without prepared claim %s", uid)
		}
		cpus = cpus.Union(allocation)
	}
	if err := cp.partitions.activate(ctx, ctr.GetId(), ctr.GetLinux().GetCgroupsPath(), cpus); err != nil {
		return fmt.Errorf("experimental partition for container %s: %w", ctr.GetId(), err)
	}
	return nil
}

func (cp *CPUDriver) synchronizePartitions(pods []*api.PodSandbox, containers []*api.Container, candidate *store.PodConfig) error {
	podsByID := make(map[string]*api.PodSandbox, len(pods))
	for _, pod := range pods {
		podsByID[pod.Id] = pod
	}
	live := make(map[string]bool, len(containers))
	for _, ctr := range containers {
		if ctr.State == api.ContainerState_CONTAINER_STOPPED {
			continue
		}
		live[ctr.Id] = true
		pod := podsByID[ctr.PodSandboxId]
		if pod == nil {
			if cp.partitions.leases[ctr.Id] != nil {
				return fmt.Errorf("live partition %s has no pod in runtime inventory", ctr.Id)
			}
			continue
		}
		state := candidate.GetContainerState(types.UID(pod.Uid), ctr.Name)
		if state == nil || !state.MatchesContainer(ctr.Name, types.UID(ctr.Id)) || !state.HasExclusiveCPUAllocation() {
			if cp.partitions.leases[ctr.Id] != nil {
				return fmt.Errorf("live partition %s lost trusted allocation state", ctr.Id)
			}
			continue
		}
		if cp.partitions.leases[ctr.Id] == nil && ctr.State == api.ContainerState_CONTAINER_CREATED {
			continue
		}
		if err := cp.partitions.verify(ctr.Id); err != nil {
			return err
		}
		path, err := partitionPath(ctr.GetLinux().GetCgroupsPath())
		if err != nil {
			return err
		}
		if cp.partitions.leases[ctr.Id].path != path {
			return fmt.Errorf("partition cgroup changed for %s", ctr.Id)
		}
	}
	// Runtime deletion may precede RemoveContainer or occur during disconnect.
	ids := make([]string, 0, len(cp.partitions.leases))
	for id := range cp.partitions.leases {
		if !live[id] {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		if err := cp.partitions.release(id); err != nil {
			return err
		}
	}
	return nil
}
