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

package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/stub"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpupartition"
	"github.com/stretchr/testify/require"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
)

type partitionRuntimePlugin struct {
	*CPUDriver
	synchronized chan error
	corrupt      atomic.Bool
}

func (p *partitionRuntimePlugin) Synchronize(ctx context.Context, pods []*api.PodSandbox, containers []*api.Container) ([]*api.ContainerUpdate, error) {
	updates, err := p.CPUDriver.Synchronize(ctx, pods, containers)
	p.synchronized <- err
	return updates, err
}

// Inject at the NRI boundary, after runc created the cgroup but before the
// driver's activation check. CRI CreateContainer alone has no cgroup yet.
func (p *partitionRuntimePlugin) StartContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) error {
	if p.corrupt.Load() {
		cg, err := cpupartition.CgroupPath(ctr.GetLinux().GetCgroupsPath())
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join("/sys/fs/cgroup", cg, "cpuset.cpus"), []byte("2\n"), 0); err != nil {
			return err
		}
	}
	return p.CPUDriver.StartContainer(ctx, pod, ctr)
}

// TestPartitionRuntime uses a real CRI runtime and NRI socket. DRA preparation
// and CDI storage are fixtures; this does not exercise kubelet or scheduling.
// Run only in a disposable VM with no other pods or NRI plugins.
func TestPartitionRuntime(t *testing.T) {
	if os.Getenv("DRACPU_PARTITION_RUNTIME_TEST") != "1" {
		t.Skip("set DRACPU_PARTITION_RUNTIME_TEST=1 in a disposable Linux VM")
	}
	require.Equal(t, 0, os.Geteuid())
	require.Equal(t, "", mustRunPartitionCRI(t, "pods", "-q"), "runtime must have no existing pod sandboxes")
	scopeName := fmt.Sprintf("dra330%d", time.Now().UnixNano())
	scope := scopeName + ".slice"
	_, scopeErr := os.Stat(filepath.Join("/sys/fs/cgroup", scope))
	require.True(t, os.IsNotExist(scopeErr), "test scope must not already exist")
	for _, qos := range []string{"guaranteed", "burstable", "besteffort"} {
		t.Run(qos, func(t *testing.T) {
			parent := scopeName + "-pod.slice"
			if qos != "guaranteed" {
				parent = scopeName + "-" + qos + "-pod.slice"
			}
			podConfig := writePartitionJSON(t, "pod.json", map[string]any{
				"metadata":      map[string]any{"name": "dra330-" + qos, "namespace": "dra330", "uid": "pod", "attempt": 0},
				"log_directory": "/tmp",
				"linux": map[string]any{
					"cgroup_parent":    parent,
					"security_context": map[string]any{"namespace_options": map[string]any{"network": 2}},
				},
			})
			sandbox := mustRunPartitionCRI(t, "runp", podConfig)
			t.Cleanup(func() {
				// Use the CRI StopPodSandbox command (not StopContainer).
				_, _ = runPartitionCRI(t, "stopp", sandbox) // codespell:ignore stopp
				_, _ = runPartitionCRI(t, "rmp", sandbox)
			})
			d := newPartitionRuntimeDriver(t, scope)
			claim := preparePartitionClaim(t, d)
			plugin, _ := connectPartitionRuntime(t, d)
			containerConfig := writePartitionJSON(t, "container.json", map[string]any{
				"metadata": map[string]any{"name": "app", "attempt": 0},
				"image":    map[string]any{"image": "registry.k8s.io/e2e-test-images/busybox:1.37.0-2"},
				"command":  []string{"/bin/sh", "-c", "echo workload-started; exec sleep 300"},
				"envs":     []map[string]string{{"key": "DRA_CPUSET_poc", "value": "1"}},
				"log_path": "dra330-" + qos + ".log",
			})
			id := mustRunPartitionCRI(t, "create", sandbox, containerConfig, podConfig)
			t.Cleanup(func() { _, _ = runPartitionCRI(t, "stop", id); _, _ = runPartitionCRI(t, "rm", id) })
			mustRunPartitionCRI(t, "start", id)
			var inspect struct {
				Info struct {
					RuntimeSpec struct {
						Linux struct {
							CgroupsPath string `json:"cgroupsPath"`
						} `json:"linux"`
					} `json:"runtimeSpec"`
				} `json:"info"`
			}
			require.NoError(t, json.Unmarshal([]byte(mustRunPartitionCRI(t, "inspect", id)), &inspect))
			cg, err := cpupartition.CgroupPath(inspect.Info.RuntimeSpec.Linux.CgroupsPath)
			require.NoError(t, err)
			read := func(file string) string {
				t.Helper()
				b, err := os.ReadFile(filepath.Join("/sys/fs/cgroup", cg, file))
				require.NoError(t, err)
				return strings.TrimSpace(string(b))
			}
			require.Equal(t, "root", read("cpuset.cpus.partition"))
			require.Equal(t, "1", read("cpuset.cpus.exclusive.effective"))
			t.Logf("running container %s partition=root CPUs=1 cgroup=%s", id, cg)
			// Reconnect a new driver while the real workload remains running.
			plugin.Stop()
			restarted := newPartitionRuntimeDriver(t, scope)
			restarted.cdiMgr = d.cdiMgr
			plugin, wrapped := connectPartitionRuntime(t, restarted)
			require.Equal(t, []string{id}, restarted.partitions.Holding(d.allocationState.GetPreparedCPUs()))
			require.Equal(t, "root", read("cpuset.cpus.partition"))
			// Withdraw an ancestor grant and exercise the driver's repair path.
			grant := filepath.Join("/sys/fs/cgroup", filepath.Dir(cg), "cpuset.cpus.exclusive")
			require.NoError(t, os.WriteFile(grant, []byte("\n"), 0))
			require.Contains(t, read("cpuset.cpus.partition"), " invalid")
			status, err := restarted.partitions.Repair()
			require.NoError(t, err)
			require.Equal(t, 1, status.Repaired, status.Errors)
			require.Equal(t, "root", read("cpuset.cpus.partition"))
			released, err := restarted.UnprepareResourceClaims(t.Context(), []kubeletplugin.NamespacedObject{{UID: claim.UID}})
			require.NoError(t, err)
			require.ErrorContains(t, released[claim.UID], "cpuset partitions")
			mustRunPartitionCRI(t, "stop", id)
			// Stop may remove the cgroup; both outcomes release the manager's lease.
			require.Empty(t, restarted.partitions.Holding(d.allocationState.GetPreparedCPUs()))
			mustRunPartitionCRI(t, "rm", id)
			released, err = restarted.UnprepareResourceClaims(t.Context(), []kubeletplugin.NamespacedObject{{UID: claim.UID}})
			require.NoError(t, err)
			require.NoError(t, released[claim.UID])
			// A failed partition activation must prevent execution, not merely log an error.
			preparePartitionClaim(t, restarted)
			id = mustRunPartitionCRI(t, "create", sandbox, containerConfig, podConfig)
			wrapped.corrupt.Store(true)
			out, startErr := runPartitionCRI(t, "start", id)
			require.Error(t, startErr, "container started despite wrong runtime cpuset: %s", out)
			require.Contains(t, startErr.Error(), "runtime cpuset")
			// The runtime must report a failed start, not a running workload.
			require.Contains(t, mustRunPartitionCRI(t, "inspect", id), "CONTAINER_EXITED")
			require.Empty(t, restarted.partitions.Holding(d.allocationState.GetPreparedCPUs()))
			t.Logf("StartContainer rejected mismatched runtime mask: %v", startErr)
			mustRunPartitionCRI(t, "rm", id)
			plugin.Stop()
			t.Logf("%s: start, cold reconnect, stop/remove and unprepare passed", qos)
		})
	}
}

func runPartitionCRI(t *testing.T, args ...string) (string, error) {
	t.Helper()
	crictl, err := exec.LookPath("crictl")
	require.NoError(t, err)
	endpoint := "unix:///run/containerd/containerd.sock"
	// Cleanup runs after t.Context is canceled; CRI calls still need a deadline.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 30*time.Second)
	defer cancel()
	// #nosec G204 -- crictl is resolved from PATH and all arguments are fixed by this test.
	cmd := exec.CommandContext(ctx, crictl, append([]string{"--runtime-endpoint", endpoint, "--image-endpoint", endpoint}, args...)...)
	cmd.Env = append(os.Environ(), "CRI_CONFIG_FILE=/dev/null")
	out, err := cmd.Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exit.Stderr)))
	}
	return strings.TrimSpace(string(out)), err
}

func mustRunPartitionCRI(t *testing.T, args ...string) string {
	t.Helper()
	out, err := runPartitionCRI(t, args...)
	require.NoError(t, err, "%v: %s", args, out)
	return out
}

func writePartitionJSON(t *testing.T, name string, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, b, 0600))
	return p
}

func newPartitionRuntimeDriver(t *testing.T, scope string) *CPUDriver {
	t.Helper()
	d, _ := newMetricsTestDriver(t)
	var err error
	d.partitions, err = cpupartition.New(cpupartition.Config{
		Mode: cpupartition.ModeRoot, CgroupRoot: "/sys/fs/cgroup", Scope: scope,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, d.partitions.Close()) })
	// The fixture claim is available on cold restart, as from CDI recovery.
	d.cdiMgr = newMockCdiMgrWithAllocations(nil)
	return d
}

func connectPartitionRuntime(t *testing.T, d *CPUDriver) (stub.Stub, *partitionRuntimePlugin) {
	t.Helper()
	wrapped := &partitionRuntimePlugin{CPUDriver: d, synchronized: make(chan error, 1)}
	p, err := stub.New(wrapped, stub.WithPluginName("dra330-test"), stub.WithPluginIdx("00"), stub.WithOnClose(func() {}))
	require.NoError(t, err)
	require.NoError(t, p.Start(t.Context()))
	t.Cleanup(p.Stop)
	select {
	case err := <-wrapped.synchronized:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("NRI Synchronize did not finish")
	}
	return p, wrapped
}
