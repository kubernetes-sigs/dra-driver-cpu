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

// Package render judges one already-rendered DaemonSet: the kubelet root has to
// reach both mount paths and the driver flag as the same directory, or the driver
// registers where the kubelet is not watching, which is issue #231. Rendering and
// the expected values come from hack/ci/helm-render-check.sh.
package render

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

const (
	driverContainer = "dracpu"
	pluginsVolume   = "device-plugin"
	registryVolume  = "plugin-registry"
	rootFlag        = "--kubelet-root-dir"
	rootFlagShort   = "-kubelet-root-dir"
)

var (
	manifest     = flag.String("manifest", "", "path to a rendered DaemonSet")
	expectedRoot = flag.String("expected-root", "", "the kubelet root the mounts and the flag must derive from")
	expectFlag   = flag.Bool("expect-root-flag", false, "whether the chart should pass "+rootFlag)
)

// validated says the checks below ran to the end. `go test -run` exits 0 when
// its pattern matches nothing, and a test that skips exits 0 too, so a renamed,
// deleted or short-circuited test would otherwise leave every caller green with
// nothing asserted.
var validated bool

func TestMain(m *testing.M) {
	code := m.Run()
	if code == 0 && *manifest != "" && !validated {
		fmt.Fprintf(os.Stderr, "no test read %s: the validator did not run\n", *manifest)
		code = 1
	}
	os.Exit(code)
}

func TestRenderedDaemonSet(t *testing.T) {
	if *manifest == "" {
		t.Skip("no -manifest given; run this through `make helm-render-check`")
	}
	ds, err := readDaemonSet(*manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkRoot(ds, *expectedRoot, *expectFlag); err != nil {
		t.Fatal(err)
	}
	validated = true
}

// readDaemonSet takes the one DaemonSet out of a rendered stream. Nothing stops
// a template file holding more than one document, and reading only the first
// would leave a second one unjudged.
//
// Documents are judged before they are given a type. Decoding straight into a
// DaemonSet drops the fields that type does not have, so a document of
// unrelated keys would arrive empty and read as a comment.
func readDaemonSet(p string) (*appsv1.DaemonSet, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	want := appsv1.SchemeGroupVersion.String()
	var found []*appsv1.DaemonSet
	r := utilyaml.NewYAMLReader(bufio.NewReader(f))
	for i := 0; ; i++ {
		doc, err := r.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%s document %d: %w", p, i, err)
		}
		if commentOnly(doc) {
			continue
		}
		raw, err := utilyaml.ToJSON(doc)
		if err != nil {
			return nil, fmt.Errorf("%s document %d: %w", p, i, err)
		}
		var tm metav1.TypeMeta
		if err := json.Unmarshal(raw, &tm); err != nil {
			return nil, fmt.Errorf("%s document %d: %w", p, i, err)
		}
		if tm.APIVersion != want || tm.Kind != "DaemonSet" {
			return nil, fmt.Errorf("%s document %d: want %s DaemonSet, got apiVersion %q and kind %q",
				p, i, want, tm.APIVersion, tm.Kind)
		}
		ds := &appsv1.DaemonSet{}
		if err := json.Unmarshal(raw, ds); err != nil {
			return nil, fmt.Errorf("%s document %d: %w", p, i, err)
		}
		found = append(found, ds)
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("%s holds %d DaemonSets, want one", p, len(found))
	}
	return found[0], nil
}

// commentOnly reports whether the document holds nothing but comments. The
// template's own licence header renders as one. An explicit null or {} is not
// one: Helm keeps both, so they have to face the check below.
func commentOnly(doc []byte) bool {
	for line := range bytes.SplitSeq(doc, []byte("\n")) {
		if t := bytes.TrimSpace(line); len(t) > 0 && !bytes.HasPrefix(t, []byte("#")) {
			return false
		}
	}
	return true
}

func checkRoot(ds *appsv1.DaemonSet, root string, wantFlag bool) error {
	c, err := driver(ds)
	if err != nil {
		return err
	}
	errs := []error{
		checkBinding(ds, c, pluginsVolume, path.Join(root, "plugins")),
		checkBinding(ds, c, registryVolume, path.Join(root, "plugins_registry")),
		checkFlag(c, root, wantFlag),
	}
	return errors.Join(errs...)
}

func driver(ds *appsv1.DaemonSet) (*corev1.Container, error) {
	var found []*corev1.Container
	for i := range ds.Spec.Template.Spec.Containers {
		if c := &ds.Spec.Template.Spec.Containers[i]; c.Name == driverContainer {
			found = append(found, c)
		}
	}
	if len(found) != 1 {
		return nil, fmt.Errorf("%d containers named %q, want one", len(found), driverContainer)
	}
	return found[0], nil
}

// checkBinding reads the mount and its volume as one: the driver writes through
// the mount and the kubelet reads the host path behind it.
func checkBinding(ds *appsv1.DaemonSet, c *corev1.Container, name, want string) error {
	var mounts []corev1.VolumeMount
	for _, m := range c.VolumeMounts {
		if m.Name == name {
			mounts = append(mounts, m)
		}
	}
	if len(mounts) != 1 {
		return fmt.Errorf("%d mounts named %q, want one", len(mounts), name)
	}
	m := mounts[0]
	var errs []error
	if m.MountPath != want {
		errs = append(errs, fmt.Errorf("mount %q is at %q, want %q", name, m.MountPath, want))
	}
	// The sockets are created here, so a partial view of the directory leaves the
	// kubelet watching a different one.
	if m.ReadOnly {
		errs = append(errs, fmt.Errorf("mount %q is read-only", name))
	}
	if m.SubPath != "" || m.SubPathExpr != "" {
		errs = append(errs, fmt.Errorf("mount %q carries a subPath", name))
	}

	var volumes []corev1.Volume
	for _, v := range ds.Spec.Template.Spec.Volumes {
		if v.Name == name {
			volumes = append(volumes, v)
		}
	}
	if len(volumes) != 1 {
		return errors.Join(append(errs, fmt.Errorf("%d volumes named %q, want one", len(volumes), name))...)
	}
	host := volumes[0].HostPath
	if host == nil {
		return errors.Join(append(errs, fmt.Errorf("volume %q is not a hostPath", name))...)
	}
	if host.Path != want {
		errs = append(errs, fmt.Errorf("volume %q points at %q, want %q", name, host.Path, want))
	}
	return errors.Join(errs...)
}

// checkFlag looks for the argument the chart manages. At the default the chart
// says nothing, leaving the root to the driver's own.
func checkFlag(c *corev1.Container, root string, want bool) error {
	var found []string
	for _, arg := range append(append([]string{}, c.Command...), c.Args...) {
		// The driver parses with the flag package, which takes one dash or two
		// and rejects more, so only those two spellings name the root. Which of
		// them the chart used is judged below.
		if name, _, _ := strings.Cut(arg, "="); name == rootFlag || name == rootFlagShort {
			found = append(found, arg)
		}
	}
	if !want {
		if len(found) != 0 {
			return fmt.Errorf("the chart passes %v at the default root, want none", found)
		}
		return nil
	}
	if len(found) != 1 {
		return fmt.Errorf("%d %s arguments, want one", len(found), rootFlag)
	}
	if got := found[0]; got != rootFlag+"="+root {
		return fmt.Errorf("the chart passes %q, want %q", got, rootFlag+"="+root)
	}
	return nil
}
