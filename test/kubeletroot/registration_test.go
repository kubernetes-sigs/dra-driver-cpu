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

// Package kubeletroot checks, on a running kind cluster, that the driver put
// its sockets under the kubelet root the chart was given. The kubelet watches
// plugins_registry under its own root, so a driver that registers anywhere else
// is never seen. Rendering the chart proves none of that, which is why this is
// separate from the render checks in test/render.
package kubeletroot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kubernetes-sigs/dra-driver-cpu/internal/driverconfig"
	testclient "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/client"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	k8swait "k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

const (
	// test/render pins this against cmd/dracpu and the chart, so a rename turns
	// that test red rather than moving these paths.
	driverName = "dra.cpu"

	sliceTimeout = 2 * time.Minute

	// Bounds one inspection, so a node that stops answering names the socket.
	inspectTimeout = 30 * time.Second
)

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// requireVariant holds the run to the job it was launched as, since the root
// alone cannot say which of the two the workflow meant. Absent is refused rather
// than defaulted, because defaulting is how it would go missing.
func requireVariant(t *testing.T, root string) {
	t.Helper()
	relocated := root != driverconfig.DefaultKubeletRootDir
	switch variant := os.Getenv("DRACPU_E2E_VARIANT"); variant {
	case "default-root":
		if relocated {
			t.Fatalf("the default-root variant is running against %q", root)
		}
	case "relocated-root":
		if !relocated {
			t.Fatalf("the relocated-root variant is running against the default root %q", root)
		}
	default:
		t.Fatalf("DRACPU_E2E_VARIANT is %q, want default-root or relocated-root", variant)
	}
}

func TestDriverRegistersUnderTheConfiguredRoot(t *testing.T) {
	// Cleaned first, since the paths below are joined onto it and joining cleans:
	// left raw, a non-canonical spelling of the default root would ask for one
	// socket to be present and then to be missing.
	root := path.Clean(envOr("DRACPU_E2E_KUBELET_ROOT_DIR", driverconfig.DefaultKubeletRootDir))
	requireVariant(t, root)
	namespace := envOr("DRACPU_E2E_NAMESPACE", "kube-system")
	name := envOr("DRACPU_E2E_DAEMONSET", "dracpu")

	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("this test reads sockets inside the kind nodes and needs docker: %v", err)
	}
	cs, err := testclient.NewK8SClientset()
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()

	// From the cluster, not from the driver's own pods: a set derived from the
	// pods would agree with itself if the DaemonSet stopped covering a node.
	nodes := clusterNodes(t, ctx, cs)
	t.Logf("checking %d node(s) against kubelet root %q", len(nodes), root)

	if got := driverNodes(t, ctx, cs, namespace, name); !slices.Equal(got, nodes) {
		t.Fatalf("driver pods run on %v, the cluster has %v", got, nodes)
	}
	waitForSliceCoverage(t, ctx, cs, nodes)

	for _, node := range nodes {
		t.Run(node, func(t *testing.T) {
			// Asserted present, not merely "not under the default": without this a
			// mistake in the paths built above would leave both sides looking clean.
			if got := statOf(t, node, registrarSocket(root)); got != "socket" {
				t.Errorf("registrar socket under %s is %s, want socket", root, got)
			}
			if got := statOf(t, node, draSocket(root)); got != "socket" {
				t.Errorf("DRA socket under %s is %s, want socket", root, got)
			}
			if root == driverconfig.DefaultKubeletRootDir {
				return
			}
			// Another kubelet plugin may legitimately keep a registrar socket under
			// the default root, so these are named rather than globbed.
			if got := statOf(t, node, registrarSocket(driverconfig.DefaultKubeletRootDir)); got != "missing" {
				t.Errorf("registrar socket under the default root is %s, want missing", got)
			}
			if got := statOf(t, node, draSocket(driverconfig.DefaultKubeletRootDir)); got != "missing" {
				t.Errorf("DRA socket under the default root is %s, want missing", got)
			}
		})
	}
}

func registrarSocket(root string) string {
	return path.Join(root, "plugins_registry", driverName+"-reg.sock")
}

// draSocket is what kubeletplugin binds while rolling updates are off, which is
// how this driver runs.
func draSocket(root string) string {
	return path.Join(root, "plugins", driverName, "dra.sock")
}

func clusterNodes(t *testing.T, ctx context.Context, cs kubernetes.Interface) []string {
	t.Helper()
	list, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range list.Items {
		names = append(names, n.Name)
	}
	if len(names) == 0 {
		t.Fatal("the cluster reported no nodes, so nothing below would have been checked")
	}
	slices.Sort(names)
	return names
}

// driverNodes returns the nodes the driver's pods run on, through the
// DaemonSet's own selector so it cannot drift from how the release was installed.
func driverNodes(t *testing.T, ctx context.Context, cs kubernetes.Interface, namespace, name string) []string {
	t.Helper()
	ds, err := cs.AppsV1().DaemonSets(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	selector, err := metav1.LabelSelectorAsSelector(ds.Spec.Selector)
	if err != nil {
		t.Fatal(err)
	}
	pods, err := cs.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, p := range pods.Items {
		if !slices.Contains(names, p.Spec.NodeName) {
			names = append(names, p.Spec.NodeName)
		}
	}
	slices.Sort(names)
	return names
}

// A rolled out DaemonSet does not mean every driver has published, and reading
// the list once would pass on the first slice from the first node.
func waitForSliceCoverage(t *testing.T, ctx context.Context, cs kubernetes.Interface, nodes []string) {
	t.Helper()
	var last []string
	err := k8swait.PollUntilContextTimeout(ctx, 2*time.Second, sliceTimeout, true, func(ctx context.Context) (bool, error) {
		list, err := cs.ResourceV1().ResourceSlices().List(ctx, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		last = nil
		for _, s := range list.Items {
			// A slice without a node name is published for the whole cluster, and
			// says nothing about which kubelet accepted the driver.
			if s.Spec.Driver != driverName || s.Spec.NodeName == nil {
				continue
			}
			if !slices.Contains(last, *s.Spec.NodeName) {
				last = append(last, *s.Spec.NodeName)
			}
		}
		slices.Sort(last)
		return slices.Equal(last, nodes), nil
	})
	if err != nil {
		t.Fatalf("%s ResourceSlices cover %v, the cluster has %v: %v", driverName, last, nodes, err)
	}
}

// An inspection that could not run is a failure of this test, never the same
// answer as the socket not being there.
const statScript = `
set -eu
p=$1
if [ -S "$p" ]; then echo socket
elif [ -e "$p" ]; then echo other
else echo missing
fi
`

func statOf(t *testing.T, node, socketPath string) string {
	t.Helper()
	return onNode(t, node, statScript, socketPath)
}

func onNode(t *testing.T, node, script, arg string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), inspectTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "exec", node, "sh", "-c", script, "sh", arg).Output()
	if err != nil {
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			t.Fatalf("reading %s on %s: %v: %s", arg, node, err, strings.TrimSpace(string(exit.Stderr)))
		}
		t.Fatalf("reading %s on %s: %v", arg, node, err)
	}
	return strings.TrimSpace(string(out))
}
