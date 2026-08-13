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

// Package render checks the DaemonSet the chart produces, for the things lint
// and schema validation cannot see: the kubelet root has to produce both mount
// paths and the driver flag from one value, or the driver registers on a path
// the kubelet never watches. No cluster needed.
package render

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/ctxlog"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/driverconfig"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

const (
	chartPath     = "../../deployment/helm/dra-driver-cpu"
	appSourcePath = "../../cmd/dracpu/app.go"

	driverContainer = "dracpu"
	pluginsVolume   = "device-plugin"
	registryVolume  = "plugin-registry"

	rootFlagName = "kubelet-root-dir"
	rootFlag     = "--" + rootFlagName

	// Bounds one render, so a helm that stops answering names the case it was on.
	renderTimeout = 60 * time.Second

	// The chart declares a kubeVersion floor, and the cluster version helm assumes
	// when none is given is its own and differs across its major releases. Pinned
	// here so the render does not depend on which helm binary answered.
	kubeVersion = "1.34.0"
)

// What the chart renders when no root is given, spelled out rather than joined
// onto the driver's constant by the same code being checked.
const (
	defaultPlugins  = "/var/lib/kubelet/plugins"
	defaultRegistry = "/var/lib/kubelet/plugins_registry"
)

func helmBin(t *testing.T) string {
	t.Helper()
	if bin := os.Getenv("HELM"); bin != "" {
		return bin
	}
	bin, err := exec.LookPath("helm")
	if err != nil {
		t.Fatalf("helm is required to render the chart, and `make helm-render-check` installs it: %v", err)
	}
	return bin
}

// Stderr is kept out of the document, so a warning helm writes never reaches
// the decoder.
func template(t *testing.T, chart string, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), renderTimeout)
	defer cancel()
	base := []string{"template", "t", chart, "--kube-version", kubeVersion}
	cmd := exec.CommandContext(ctx, helmBin(t), append(base, args...)...) //nolint:gosec // the helm binary is whichever one the caller put on PATH or in HELM
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func daemonSet(t *testing.T, args ...string) *appsv1.DaemonSet {
	t.Helper()
	return daemonSetFromChart(t, chartPath, args...)
}

func daemonSetFromChart(t *testing.T, chart string, args ...string) *appsv1.DaemonSet {
	t.Helper()
	out, err := template(t, chart, args...)
	if err != nil {
		t.Fatalf("render failed: %v", err)
	}
	ds, err := findDaemonSet(out)
	if err != nil {
		t.Fatalf("reading the rendered chart: %v", err)
	}
	return ds
}

// Reads the whole render, so a second DaemonSet is reported rather than
// whichever came first being taken.
func findDaemonSet(doc []byte) (*appsv1.DaemonSet, error) {
	reader := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(doc)))
	var found []*appsv1.DaemonSet
	for {
		chunk, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		var meta metav1.TypeMeta
		if err := yaml.Unmarshal(chunk, &meta); err != nil {
			return nil, fmt.Errorf("reading a rendered document: %w", err)
		}
		if meta.Kind != "DaemonSet" {
			continue
		}
		if meta.APIVersion != appsv1.SchemeGroupVersion.String() {
			return nil, fmt.Errorf("DaemonSet has apiVersion %q, want %q", meta.APIVersion, appsv1.SchemeGroupVersion.String())
		}
		ds := &appsv1.DaemonSet{}
		if err := yaml.Unmarshal(chunk, ds); err != nil {
			return nil, err
		}
		found = append(found, ds)
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("the render contains %d DaemonSets, want exactly one", len(found))
	}
	return found[0], nil
}

func container(t *testing.T, ds *appsv1.DaemonSet) *corev1.Container {
	t.Helper()
	var found []*corev1.Container
	for i := range ds.Spec.Template.Spec.Containers {
		if c := &ds.Spec.Template.Spec.Containers[i]; c.Name == driverContainer {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%d containers named %q, want one", len(found), driverContainer)
	}
	return found[0]
}

// The four places the root has to reach, read by name so a swap between them is
// still caught.
type bindings struct {
	pluginsMount  string
	registryMount string
	pluginsHost   string
	registryHost  string
	allMountPaths []string
	allHostPaths  []string
	// how often each named mount and volume appeared, so a duplicate is reported
	// rather than hidden behind whichever was read last.
	counts   map[string]int
	notHosts []string
	notWhole []string
}

// readBindings gathers; check judges.
func readBindings(t *testing.T, ds *appsv1.DaemonSet) bindings {
	t.Helper()
	b := bindings{counts: map[string]int{}}
	for _, m := range container(t, ds).VolumeMounts {
		b.allMountPaths = append(b.allMountPaths, m.MountPath)
		switch m.Name {
		case pluginsVolume:
			b.counts["volumeMount "+pluginsVolume]++
			b.pluginsMount = m.MountPath
		case registryVolume:
			b.counts["volumeMount "+registryVolume]++
			b.registryMount = m.MountPath
		default:
			continue
		}
		// A subPath mounts a directory inside the volume rather than the volume,
		// and read-only leaves nowhere to create a socket, so either puts the
		// driver's sockets somewhere other than the host path checked below.
		if m.ReadOnly || m.SubPath != "" || m.SubPathExpr != "" {
			b.notWhole = append(b.notWhole, m.Name)
		}
	}
	for _, v := range ds.Spec.Template.Spec.Volumes {
		// Counted by name before the source is read, so a second volume under
		// one of these names cannot slip past by not being a hostPath.
		if v.Name == pluginsVolume || v.Name == registryVolume {
			b.counts["volume "+v.Name]++
			if v.HostPath == nil {
				b.notHosts = append(b.notHosts, v.Name)
				continue
			}
		}
		if v.HostPath == nil {
			continue
		}
		b.allHostPaths = append(b.allHostPaths, v.HostPath.Path)
		switch v.Name {
		case pluginsVolume:
			b.pluginsHost = v.HostPath.Path
		case registryVolume:
			b.registryHost = v.HostPath.Path
		}
	}
	return b
}

func (b bindings) check(t *testing.T, plugins, registry string) {
	t.Helper()
	for _, name := range b.notHosts {
		t.Errorf("volume %s is not a hostPath", name)
	}
	for _, name := range b.notWhole {
		t.Errorf("volumeMount %s is not a writable mount of the whole volume", name)
	}
	for _, what := range []string{
		"volumeMount " + pluginsVolume, "volumeMount " + registryVolume,
		"volume " + pluginsVolume, "volume " + registryVolume,
	} {
		if b.counts[what] != 1 {
			t.Errorf("%s appears %d times, want once", what, b.counts[what])
		}
	}
	for _, got := range []struct {
		what string
		have string
		want string
	}{
		{"volumeMount " + pluginsVolume, b.pluginsMount, plugins},
		{"volumeMount " + registryVolume, b.registryMount, registry},
		{"volume " + pluginsVolume, b.pluginsHost, plugins},
		{"volume " + registryVolume, b.registryHost, registry},
	} {
		if got.have != got.want {
			t.Errorf("%s is %q, want %q", got.what, got.have, got.want)
		}
	}
	// A half migration moves one and leaves the other, so every path the pod
	// carries is checked, not only the two named above.
	for _, stale := range []string{defaultPlugins, defaultRegistry} {
		if stale == plugins || stale == registry {
			continue
		}
		if slices.Contains(b.allMountPaths, stale) || slices.Contains(b.allHostPaths, stale) {
			t.Errorf("the default path %q is still bound", stale)
		}
	}
}

// The image sets no entrypoint, so a container without a command runs its
// arguments as the whole command line and the first of them is argv0.
func commandLine(c *corev1.Container) ([]string, error) {
	argv := append(slices.Clone(c.Command), c.Args...)
	if len(argv) == 0 {
		return nil, errors.New("the container carries no command line")
	}
	if strings.HasPrefix(argv[0], "-") {
		return nil, fmt.Errorf("the command line starts with %q, so there is no program to run", argv[0])
	}
	return argv, nil
}

// counter reports how often the parser was handed a flag. Visit reports a
// repeated flag once, and a render is meant to carry this one at most once.
type counter struct {
	flag.Value
	n int
}

func (c *counter) Set(s string) error {
	c.n++
	return c.Value.Set(s)
}

// The usage printer asks a fresh zero of this type for its string, and the
// embedded interface is nil there.
func (c *counter) String() string {
	if c == nil || c.Value == nil {
		return ""
	}
	return c.Value.String()
}

// renderedRoot answers what the driver would run on, or why it would not get
// that far. It registers what cmd/dracpu registers and parses from the same
// offset, so the answer comes from the parser the render will meet rather than
// a second implementation of Go's flag grammar.
func renderedRoot(c *corev1.Container) (root string, sent bool, err error) {
	argv, err := commandLine(c)
	if err != nil {
		return "", false, err
	}
	var cfg driverconfig.Config
	var configFile string
	fs := flag.NewFlagSet(argv[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&configFile, "config", "", "")
	cfg.AddFlags(fs)
	ctxlog.AddFlags(fs)
	registered := fs.Lookup(rootFlagName)
	if registered == nil {
		return "", false, fmt.Errorf("the driver has no %s flag, which the chart renders", rootFlagName)
	}
	seen := &counter{Value: registered.Value}
	registered.Value = seen
	if err := fs.Parse(argv[1:]); err != nil {
		return "", false, err
	}
	// Whatever stops the parse is read as a subcommand, which cmd/dracpu refuses
	// to run alongside flags. An argument the driver does not take as a flag ends
	// the process rather than leaving it on the default root.
	if rest := fs.Args(); len(rest) > 0 {
		return "", false, fmt.Errorf("%v was not parsed as a flag", rest)
	}
	if seen.n > 1 {
		return "", false, fmt.Errorf("%s appears %d times and the driver keeps the last", rootFlagName, seen.n)
	}
	return cfg.KubeletRootDir, seen.n == 1, nil
}

func driverRoot(t *testing.T, ds *appsv1.DaemonSet) (string, bool) {
	t.Helper()
	root, sent, err := renderedRoot(container(t, ds))
	if err != nil {
		t.Fatalf("the driver would not run the rendered command line: %v", err)
	}
	return root, sent
}

// The whole rendered path is asserted rather than a root plus a suffix: with
// root "/" the two differ, and that is the case worth pinning.
func TestKubeletRootDerivesTheMountsAndTheFlag(t *testing.T) {
	for _, tc := range []struct {
		name     string
		root     string
		plugins  string
		registry string
		flag     string
	}{
		{
			name:     "plain root",
			root:     "/mnt/fast/k8s/kubelet",
			plugins:  "/mnt/fast/k8s/kubelet/plugins",
			registry: "/mnt/fast/k8s/kubelet/plugins_registry",
			flag:     "/mnt/fast/k8s/kubelet",
		},
		{
			name:     "root needing cleaning",
			root:     "/mnt/a/../kubelet//",
			plugins:  "/mnt/kubelet/plugins",
			registry: "/mnt/kubelet/plugins_registry",
			flag:     "/mnt/kubelet",
		},
		{
			name:     "filesystem root",
			root:     "/",
			plugins:  "/plugins",
			registry: "/plugins_registry",
			flag:     "/",
		},
		{
			name:     "root holding a hash",
			root:     "/mnt/with #tag/kubelet",
			plugins:  "/mnt/with #tag/kubelet/plugins",
			registry: "/mnt/with #tag/kubelet/plugins_registry",
			flag:     "/mnt/with #tag/kubelet",
		},
		{
			name:     "root holding a colon",
			root:     "/mnt/with: colon/kubelet",
			plugins:  "/mnt/with: colon/kubelet/plugins",
			registry: "/mnt/with: colon/kubelet/plugins_registry",
			flag:     "/mnt/with: colon/kubelet",
		},
		{
			name:     "root with many parent references",
			root:     "/mnt/kubelet" + strings.Repeat("/x/..", 16) + "/",
			plugins:  "/mnt/kubelet/plugins",
			registry: "/mnt/kubelet/plugins_registry",
			flag:     "/mnt/kubelet",
		},
		{
			// Renders paths that contain the default one, which is why the stale
			// check compares whole paths rather than looking for a substring.
			name:     "root prefixed by the default plugins path",
			root:     "/var/lib/kubelet/plugins-alt",
			plugins:  "/var/lib/kubelet/plugins-alt/plugins",
			registry: "/var/lib/kubelet/plugins-alt/plugins_registry",
			flag:     "/var/lib/kubelet/plugins-alt",
		},
		{
			name:     "root landing exactly on the socket budget",
			root:     "/mnt/kubelet-root-of-exactly-73-bytes-pins-the-socket-budget-xxxxxxxxxxxx",
			plugins:  "/mnt/kubelet-root-of-exactly-73-bytes-pins-the-socket-budget-xxxxxxxxxxxx/plugins",
			registry: "/mnt/kubelet-root-of-exactly-73-bytes-pins-the-socket-budget-xxxxxxxxxxxx/plugins_registry",
			flag:     "/mnt/kubelet-root-of-exactly-73-bytes-pins-the-socket-budget-xxxxxxxxxxxx",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds := daemonSet(t, "--set-string", "kubeletRootDir="+tc.root)
			readBindings(t, ds).check(t, tc.plugins, tc.registry)
			got, sent := driverRoot(t, ds)
			if !sent {
				t.Fatalf("no %s reached the driver", rootFlag)
			}
			if got != tc.flag {
				t.Errorf("the driver starts on %q, want %q", got, tc.flag)
			}
		})
	}
}

// The default sends no flag: it is newer than the chart's appVersion, so the
// default image would exit on an unknown flag.
func TestDefaultRootSendsNoFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"value unset", nil},
		// The comparison runs on the cleaned root, so moving it ahead of the
		// cleaning would leave the table above green and start sending a flag here.
		{"root cleaning back to the default", []string{"--set-string", "kubeletRootDir=/var/lib/kubelet/./"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds := daemonSet(t, tc.args...)
			readBindings(t, ds).check(t, defaultPlugins, defaultRegistry)
			if got, sent := driverRoot(t, ds); sent {
				t.Errorf("%s=%q reached an image that predates the flag", rootFlag, got)
			}
		})
	}
}

// A root the chart cannot honour is refused whether or not the values schema
// runs, since the schema rejects most of these before the template's own guards
// are reached. Which layer refuses a root is deliberately not asserted.
func TestUnusableRootIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name    string
		root    string
		refusal string
	}{
		{"empty", "", "must not be empty"},
		{"blank", "  ", "must not begin or end with whitespace"},
		{"leading space", " /mnt/x", "must not begin or end with whitespace"},
		{"trailing space", "/mnt/x ", "must not begin or end with whitespace"},
		{"relative", "relative/x", "must be an absolute path"},
		{"over the byte budget", "/mnt/kubelet-root-long-enough-to-overrun-the-sun-path-budget-0123456789012345", "over the 107-byte limit"},
		{"one byte over the registrar socket limit", "/mnt/kubelet-root-of-exactly-74-bytes-pins-the-socket-budget-xxxxxxxxxxxxx", "over the 107-byte limit"},
		{"multibyte inside the character limit", "/" + strings.Repeat("界", 30), "over the 107-byte limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forEachSchemaMode(t, func(t *testing.T, mode []string, schemaOff bool) {
				_, err := template(t, chartPath, append(mode, "--set-string", "kubeletRootDir="+tc.root)...)
				if err == nil {
					t.Fatalf("root %q was accepted", tc.root)
				}
				// Only the schema-off run reaches the chart's own guard, and its
				// message is what tells this case apart from the other seven.
				if schemaOff && !strings.Contains(err.Error(), tc.refusal) {
					t.Errorf("root %q was refused by %q, want %q", tc.root, err, tc.refusal)
				}
			})
		})
	}
}

// A top-level value differing from kubeletRootDir only in case is refused
// rather than dropped, and by the template in both modes, so a schema change is
// never the only thing between a misspelling and a silent revert to the default.
func TestMiscasedRootKeyIsRefused(t *testing.T) {
	for _, key := range []string{"kubeletrootdir", "KubeletRootDir", "kubeletRootdir", "KUBELETROOTDIR"} {
		t.Run(key, func(t *testing.T) {
			forEachSchemaMode(t, func(t *testing.T, mode []string, schemaOff bool) {
				_, err := template(t, chartPath, append(mode, "--set-string", key+"=/mnt/data/kubelet")...)
				if err == nil {
					t.Fatalf("key %q was accepted", key)
				}
				if schemaOff && !strings.Contains(err.Error(), "differs from kubeletRootDir only in case") {
					t.Errorf("key %q was refused by %q, want the case-collision guard", key, err)
				}
			})
		})
	}
}

// driverConfig is the one value that changes the rendered command line and adds
// a mount of its own, and both are places this could fail on a chart that is
// fine: an argument the driver does not register, and a read-only mount this
// does not manage.
func TestDriverConfigLeavesTheRootWiringAlone(t *testing.T) {
	ds := daemonSet(t,
		"--set-string", "kubeletRootDir=/mnt/data/kubelet",
		"--set-string", "driverConfig.reservedCPUs=0-1")
	readBindings(t, ds).check(t, "/mnt/data/kubelet/plugins", "/mnt/data/kubelet/plugins_registry")
	got, sent := driverRoot(t, ds)
	if !sent || got != "/mnt/data/kubelet" {
		t.Errorf("the driver starts on %q, sent=%v, want /mnt/data/kubelet", got, sent)
	}
}

// The spelling the chart does render, so the guard above cannot be passing by
// refusing everything.
func TestCorrectlySpelledRootKeyStillReachesTheDriver(t *testing.T) {
	ds := daemonSet(t, "--skip-schema-validation", "--set-string", "kubeletRootDir=/mnt/data/kubelet")
	got, sent := driverRoot(t, ds)
	if !sent || got != "/mnt/data/kubelet" {
		t.Errorf("the driver starts on %q, sent=%v, want /mnt/data/kubelet", got, sent)
	}
}

func forEachSchemaMode(t *testing.T, run func(t *testing.T, mode []string, schemaOff bool)) {
	t.Helper()
	t.Run("with the values schema", func(t *testing.T) { run(t, nil, false) })
	t.Run("with the values schema skipped", func(t *testing.T) {
		run(t, []string{"--skip-schema-validation"}, true)
	})
}

// extraArgs are spliced in after the managed flag and the driver takes the last
// value, so one naming the root would win it while the mounts kept rendering
// from the chart value. One dash and two are the same flag to the driver.
func TestExtraArgsCannotNameTheManagedFlag(t *testing.T) {
	for _, tc := range []struct {
		arg      string
		accepted bool
	}{
		{"--kubelet-root-dir=/other", false},
		{"-kubelet-root-dir=/other", false},
		{"--kubelet-root-dir", false},
		{"-kubelet-root-dir", false},
		{"--kubelet-root-directory", true},
	} {
		t.Run(tc.arg, func(t *testing.T) {
			_, err := template(t, chartPath, "--set-string", "extraArgs[0]="+tc.arg)
			if accepted := err == nil; accepted != tc.accepted {
				t.Errorf("extraArgs %q accepted=%v, want %v", tc.arg, accepted, tc.accepted)
			}
		})
	}
}

// An upgrade run with --reuse-values passes the values the release was installed
// with, so a release older than this value has no key at all. Rendering a copy
// with the key removed is the only way to reach that state.
func TestAbsentRootValueRendersTheDefault(t *testing.T) {
	chart := copyChartWithoutRoot(t)
	ds := daemonSetFromChart(t, chart)
	readBindings(t, ds).check(t, defaultPlugins, defaultRegistry)
	if got, sent := driverRoot(t, ds); sent {
		t.Errorf("%s=%q reached the driver from a chart with no root value", rootFlag, got)
	}
}

// Helm drops a key set to null while coalescing, before the values schema is
// applied, so null arrives at the helper as an absent key does. The two cannot
// be told apart here, and this pins that as the answer.
func TestNullRootValueRendersTheDefault(t *testing.T) {
	values := filepath.Join(t.TempDir(), "null-root.yaml")
	if err := os.WriteFile(values, []byte("kubeletRootDir: null\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ds := daemonSet(t, "-f", values)
	readBindings(t, ds).check(t, defaultPlugins, defaultRegistry)
	if got, sent := driverRoot(t, ds); sent {
		t.Errorf("%s=%q reached the driver from a null root value", rootFlag, got)
	}
}

func copyChartWithoutRoot(t *testing.T) string {
	t.Helper()
	dst := filepath.Join(t.TempDir(), filepath.Base(chartPath))
	if err := os.CopyFS(dst, os.DirFS(chartPath)); err != nil {
		t.Fatal(err)
	}
	valuesPath := filepath.Join(dst, "values.yaml")
	raw, err := os.ReadFile(valuesPath)
	if err != nil {
		t.Fatal(err)
	}
	var values map[string]any
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	if _, ok := values["kubeletRootDir"]; !ok {
		t.Fatal("values.yaml has no kubeletRootDir, so this case would read the chart default")
	}
	delete(values, "kubeletRootDir")
	edited, err := yaml.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(valuesPath, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	return dst
}

// The chart spells the registrar socket out while the driver builds it from
// driverName, and its length is what the root's byte budget is left over from.
// A rename would move the budget in one and not the other.
func TestRegistrarSocketNameFollowsDriverName(t *testing.T) {
	name := driverNameFromSource(t)
	helpers, err := os.ReadFile(filepath.Join(chartPath, "templates", "_helpers.tpl"))
	if err != nil {
		t.Fatal(err)
	}
	socket := "/plugins_registry/" + name + "-reg.sock"
	if !strings.Contains(string(helpers), socket) {
		t.Errorf("the chart does not spell the registrar socket %q, which driverName %q gives", socket, name)
	}
}

// Four places have to agree on the default root: the driver's constant, the
// chart value, the template's test for whether to send the flag, and the
// helper's answer when the value is absent. No flag is sent at the default, so
// a one-sided change leaves nothing in the render to say the two disagree.
func TestDefaultRootAgreesEverywhere(t *testing.T) {
	var values struct {
		KubeletRootDir string `json:"kubeletRootDir"`
	}
	raw, err := os.ReadFile(filepath.Join(chartPath, "values.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(raw, &values); err != nil {
		t.Fatal(err)
	}
	for _, got := range []struct {
		where string
		value string
	}{
		{"values.yaml", values.KubeletRootDir},
		{"templates/daemonset.yaml", quotedAfter(t, filepath.Join(chartPath, "templates", "daemonset.yaml"), `ne $kubeletRoot `)},
		{"templates/_helpers.tpl", quotedAfter(t, filepath.Join(chartPath, "templates", "_helpers.tpl"), `{{- $root := `)},
	} {
		if got.value != driverconfig.DefaultKubeletRootDir {
			t.Errorf("%s says %q, the driver says %q", got.where, got.value, driverconfig.DefaultKubeletRootDir)
		}
	}
}

// The chart's default lives in template syntax, so text is the only way to read it.
func quotedAfter(t *testing.T, path, marker string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(raw), marker+`"`)
	if !ok {
		t.Fatalf("%s no longer contains %q", path, marker)
	}
	value, _, ok := strings.Cut(rest, `"`)
	if !ok {
		t.Fatalf("unterminated string after %q in %s", marker, path)
	}
	return value
}

// cmd/dracpu is package main, so the constant cannot be imported. Parsed rather
// than matched by text, so a reformat cannot quietly turn this check off.
func driverNameFromSource(t *testing.T) string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), appSourcePath, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var name string
	ast.Inspect(file, func(n ast.Node) bool {
		spec, ok := n.(*ast.ValueSpec)
		if !ok || len(spec.Names) != 1 || spec.Names[0].Name != "driverName" || len(spec.Values) != 1 {
			return true
		}
		lit, ok := spec.Values[0].(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if unquoted, err := strconv.Unquote(lit.Value); err == nil {
			name = unquoted
		}
		return false
	})
	if name == "" {
		t.Fatalf("no driverName constant found in %s", appSourcePath)
	}
	return name
}

// The cases below are given their input directly, because the chart cannot
// render the shapes they exist to refuse.

func daemonSetWith(mounts []corev1.VolumeMount, volumes []corev1.Volume) *appsv1.DaemonSet {
	return &appsv1.DaemonSet{Spec: appsv1.DaemonSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
		Containers: []corev1.Container{{Name: driverContainer, VolumeMounts: mounts}},
		Volumes:    volumes,
	}}}}
}

// The last case is a mount the chart also carries and this does not manage, so
// the three above cannot be passing by refusing every read-only mount.
func TestManagedMountsAreRequiredToCarryTheWholeVolume(t *testing.T) {
	for _, tc := range []struct {
		name  string
		mount corev1.VolumeMount
		want  []string
	}{
		{"read-only", corev1.VolumeMount{Name: pluginsVolume, ReadOnly: true}, []string{pluginsVolume}},
		{"a subPath", corev1.VolumeMount{Name: registryVolume, SubPath: "inner"}, []string{registryVolume}},
		{"a subPathExpr", corev1.VolumeMount{Name: registryVolume, SubPathExpr: "$(POD_NAME)"}, []string{registryVolume}},
		{"read-only under a name this does not manage", corev1.VolumeMount{Name: "nri-plugin", ReadOnly: true}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := readBindings(t, daemonSetWith([]corev1.VolumeMount{tc.mount}, nil))
			if diff := cmp.Diff(tc.want, b.notWhole); diff != "" {
				t.Errorf("mounts reported as not carrying the whole volume (-want +got):\n%s", diff)
			}
		})
	}
}

// Every shape below is one line in a template, and they part company only at
// the driver.
func TestRenderedArgumentsAreReadAsTheDriverReadsThem(t *testing.T) {
	args := func(a ...string) corev1.Container { return corev1.Container{Args: a} }
	for _, tc := range []struct {
		name      string
		container corev1.Container
		root      string
		sent      bool
		refusal   string
	}{
		{name: "joined, two dashes", container: args("/dracpu", "--kubelet-root-dir=/a"), root: "/a", sent: true},
		{name: "joined, one dash", container: args("/dracpu", "-kubelet-root-dir=/a"), root: "/a", sent: true},
		{name: "separated, two dashes", container: args("/dracpu", "--kubelet-root-dir", "/a"), root: "/a", sent: true},
		{name: "separated, one dash", container: args("/dracpu", "-kubelet-root-dir", "/a"), root: "/a", sent: true},
		{name: "no flag leaves the driver's own default", container: args("/dracpu", "--v=4"), root: driverconfig.DefaultKubeletRootDir},
		{
			name:      "a command and its arguments",
			container: corev1.Container{Command: []string{"/dracpu"}, Args: []string{"--kubelet-root-dir=/a"}},
			root:      "/a", sent: true,
		},
		// The driver would start on /b while the mounts rendered from whichever
		// value the chart meant, so two is refused rather than resolved.
		{name: "the flag twice", container: args("/dracpu", "--kubelet-root-dir=/a", "-kubelet-root-dir", "/b"), refusal: "appears 2 times"},
		{name: "no dashes is not a flag", container: args("/dracpu", "kubelet-root-dir=/a"), refusal: "was not parsed as a flag"},
		{name: "three dashes is not a flag", container: args("/dracpu", "---kubelet-root-dir=/a"), refusal: "bad flag syntax"},
		{name: "nothing after the terminator is a flag", container: args("/dracpu", "--", "--kubelet-root-dir=/a"), refusal: "was not parsed as a flag"},
		{name: "a plain argument ends the flags", container: args("/dracpu", "x", "--kubelet-root-dir=/a"), refusal: "was not parsed as a flag"},
		{name: "a near match is a flag the driver does not have", container: args("/dracpu", "--kubelet-root-directory=/a"), refusal: "not defined"},
		{name: "nothing to run", container: corev1.Container{}, refusal: "no command line"},
		{name: "a command line that is only flags", container: args("--kubelet-root-dir=/a"), refusal: "no program to run"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root, sent, err := renderedRoot(&tc.container)
			if tc.refusal != "" {
				if err == nil {
					t.Fatalf("accepted, reading root %q", root)
				}
				if !strings.Contains(err.Error(), tc.refusal) {
					t.Errorf("refused with %q, want it to mention %q", err, tc.refusal)
				}
				return
			}
			if err != nil {
				t.Fatalf("refused: %v", err)
			}
			if root != tc.root || sent != tc.sent {
				t.Errorf("root %q sent=%v, want %q sent=%v", root, sent, tc.root, tc.sent)
			}
		})
	}
}

func TestNamedVolumesAreCountedBeforeTheirSource(t *testing.T) {
	b := readBindings(t, daemonSetWith(nil, []corev1.Volume{
		{Name: pluginsVolume, VolumeSource: corev1.VolumeSource{HostPath: &corev1.HostPathVolumeSource{Path: "/p"}}},
		{Name: pluginsVolume, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
	}))
	if got := b.counts["volume "+pluginsVolume]; got != 2 {
		t.Errorf("%s counted %d times, want 2: a duplicate that is not a hostPath still occupies the name", pluginsVolume, got)
	}
	if diff := cmp.Diff([]string{pluginsVolume}, b.notHosts); diff != "" {
		t.Errorf("volumes reported as not hostPaths (-want +got):\n%s", diff)
	}
}

func TestFindDaemonSetRefusesWhatItCannotVouchFor(t *testing.T) {
	for _, tc := range []struct {
		name    string
		doc     string
		refusal string
	}{
		// The reason is checked, not only that something was refused: skipping
		// the document instead would still end in "contains 0 DaemonSets".
		{"a document that will not decode", "kind: Something\nmetadata:\n  name: [\n", "reading a rendered document"},
		{"a DaemonSet from another API version", "apiVersion: extensions/v1beta1\nkind: DaemonSet\nmetadata:\n  name: d\n", "apiVersion"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := findDaemonSet([]byte(tc.doc))
			if err == nil {
				t.Fatal("accepted")
			}
			if !strings.Contains(err.Error(), tc.refusal) {
				t.Errorf("refused with %q, want it to mention %q", err, tc.refusal)
			}
		})
	}
}
