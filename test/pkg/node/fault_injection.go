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

package node

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"time"
)

// The kubelet API does not expose faults like "restart the kubelet" or "reboot
// the node", so tests inject them by acting on the container which backs the
// node. This works on kind-like clusters, where every node is a container
// running on the same host as the test binary and named after the node itself.

// runtimeEnvVar names the environment variable which overrides the container
// runtime CLI used for fault injection.
const runtimeEnvVar = "DRACPU_E2E_NODE_RUNTIME"

const defaultRuntime = "docker"

// cdiSpecDir is the directory the container runtime reads CDI specs from, and
// the hostPath the driver writes them to. It lives on the node and, on most
// nodes, on a tmpfs, so its content does not survive a reboot.
const cdiSpecDir = "/var/run/cdi"

// FaultInjector injects node-level faults, such as a kubelet restart or a node
// reboot, by driving the container runtime on the test host.
type FaultInjector struct {
	runtime  string
	nodeName string
}

// NewFaultInjector returns a FaultInjector for the node with the given name.
// It fails if the container runtime CLI is not available, or if no container
// named after the node exists. Callers use that failure to detect that node
// fault injection is not supported by the current cluster.
func NewFaultInjector(ctx context.Context, nodeName string) (*FaultInjector, error) {
	runtime := os.Getenv(runtimeEnvVar)
	if runtime == "" {
		runtime = defaultRuntime
	}
	runtimePath, err := exec.LookPath(runtime)
	if err != nil {
		return nil, fmt.Errorf("container runtime %q is not available: %w", runtime, err)
	}

	fi := &FaultInjector{runtime: runtimePath, nodeName: nodeName}
	if _, err := fi.run(ctx, "inspect", "--format", "{{.State.Status}}", nodeName); err != nil {
		return nil, fmt.Errorf("no container found for node %q: %w", nodeName, err)
	}
	return fi, nil
}

// NodeName returns the name of the node the injector acts on.
func (fi *FaultInjector) NodeName() string {
	return fi.nodeName
}

// Restart reboots the node by restarting the container which backs it. It
// returns once the container runtime reports the container as running again.
func (fi *FaultInjector) Restart(ctx context.Context) error {
	if _, err := fi.run(ctx, "restart", fi.nodeName); err != nil {
		return fmt.Errorf("failed to restart node %q: %w", fi.nodeName, err)
	}
	return nil
}

// Exec runs a command inside the node and returns its standard output.
func (fi *FaultInjector) Exec(ctx context.Context, command ...string) (string, error) {
	args := append([]string{"exec", fi.nodeName}, command...)
	out, err := fi.run(ctx, args...)
	if err != nil {
		return out, fmt.Errorf("failed to run %v on node %q: %w", command, fi.nodeName, err)
	}
	return out, nil
}

// RestartKubelet restarts the kubelet service inside the node.
func (fi *FaultInjector) RestartKubelet(ctx context.Context) error {
	_, err := fi.Exec(ctx, "systemctl", "restart", "kubelet")
	return err
}

// KubeletActive reports whether the kubelet service inside the node is active.
// It uses "systemctl show" because "systemctl is-active" reports inactive and
// activating states through the exit code.
func (fi *FaultInjector) KubeletActive(ctx context.Context) (bool, error) {
	out, err := fi.Exec(ctx, "systemctl", "show", "--property=ActiveState", "--value", "kubelet")
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "active", nil
}

// ListCDISpecs returns the file names found in the node's CDI spec directory.
// A missing directory is reported as an empty list rather than as an error: the
// kubelet creates it when it mounts the hostPath, so it is legitimately absent
// on a node which never prepared a claim.
func (fi *FaultInjector) ListCDISpecs(ctx context.Context) ([]string, error) {
	// "test" reports a false condition through exit code 1, so a non-zero code
	// other than 1 means the check itself could not run.
	_, _, exitCode, err := fi.runCommand(ctx, "exec", fi.nodeName, "test", "-d", cdiSpecDir)
	if err != nil {
		return nil, fmt.Errorf("failed to check for %q on node %q: %w", cdiSpecDir, fi.nodeName, err)
	}
	if exitCode == 1 {
		return nil, nil
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("checking for %q on node %q failed with exit code %d", cdiSpecDir, fi.nodeName, exitCode)
	}

	out, err := fi.Exec(ctx, "ls", "-1", cdiSpecDir)
	if err != nil {
		return nil, err
	}

	var names []string
	for line := range strings.SplitSeq(out, "\n") {
		if name := strings.TrimSpace(line); name != "" {
			names = append(names, name)
		}
	}
	return names, nil
}

// CDISpecModTime returns the modification time of a file in the node's CDI spec
// directory. It lets a caller tell a spec the driver wrote again from one that
// merely survived, which is the difference between a node reboot having wiped
// the directory and nothing having happened.
func (fi *FaultInjector) CDISpecModTime(ctx context.Context, name string) (time.Time, error) {
	out, err := fi.Exec(ctx, "stat", "--format", "%Y", path.Join(cdiSpecDir, name))
	if err != nil {
		return time.Time{}, err
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("cannot parse the modification time %q of %q on node %q: %w", out, name, fi.nodeName, err)
	}
	return time.Unix(seconds, 0), nil
}

func (fi *FaultInjector) run(ctx context.Context, args ...string) (string, error) {
	out, stderr, exitCode, err := fi.runCommand(ctx, args...)
	if err != nil {
		return out, err
	}
	if exitCode != 0 {
		if stderr != "" {
			return out, fmt.Errorf("command exited with code %d: %s", exitCode, stderr)
		}
		return out, fmt.Errorf("command exited with code %d", exitCode)
	}
	return out, nil
}

// runCommand runs the container runtime CLI and reports its standard output,
// its standard error and its exit code. A non-zero exit code is not an error
// here: callers decide whether the code is expected.
func (fi *FaultInjector) runCommand(ctx context.Context, args ...string) (string, string, int, error) {
	cmd := exec.CommandContext(ctx, fi.runtime, args...) //nolint:gosec // the runtime CLI and its arguments come from the test, not from user input
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
			return string(out), strings.TrimSpace(string(exitErr.Stderr)), exitErr.ExitCode(), nil
		}
		return string(out), "", -1, err
	}
	return string(out), "", 0, nil
}
