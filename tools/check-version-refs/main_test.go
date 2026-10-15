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

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
)

func TestTargetRelease(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
		wantErr string
	}{
		{
			name:    "release",
			version: "v0.3.0",
			want:    "0.3.0",
		},
		{
			name:    "without the v prefix",
			version: "0.3.0",
			want:    "0.3.0",
		},
		{
			// Comparing against a target that does not parse would accept
			// every reference, which is the one failure this check must not
			// have.
			name:    "pre-release",
			version: "v0.4.0-rc.1",
			wantErr: "not a major.minor.patch release",
		},
		{
			name:    "empty",
			version: "",
			wantErr: "not a major.minor.patch release",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := targetRelease(tc.version)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected an error containing %q, got %v", tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected an error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.String() != tc.want {
				t.Fatalf("expected %s, got %s", tc.want, got)
			}
		})
	}
}

func TestNamesOlderRelease(t *testing.T) {
	target, err := semver.NewVersion("0.3.0")
	if err != nil {
		t.Fatalf("parse target version: %v", err)
	}
	tests := []struct {
		name string
		line string
		want bool
	}{
		{
			name: "older minor",
			line: "helm install --version 0.2.0 -n kube-system",
			want: true,
		},
		{
			name: "older patch",
			line: "the tag is v0.2.9",
			want: true,
		},
		{
			name: "older literal without the v prefix",
			line: "upgrade from 0.2.0",
			want: true,
		},
		{
			name: "current release",
			line: "the tag is v0.3.0",
			want: false,
		},
		{
			name: "newer patch",
			line: "the tag is v0.3.1",
			want: false,
		},
		{
			name: "newer minor",
			line: "upgrade to v0.4.0",
			want: false,
		},
		{
			// 10 is newer than 3, though "10" sorts before "3" as a string.
			name: "two digit minor",
			line: "the tag is v0.10.0",
			want: false,
		},
		{
			name: "kubernetes release",
			line: "see kubernetes v1.35.0 for the upstream policy",
			want: false,
		},
		{
			name: "api version",
			line: "the attribute was introduced in v1.0.0",
			want: false,
		},
		{
			name: "go toolchain",
			line: "goVersion: go1.26.0",
			want: false,
		},
		{
			name: "ipv4 address",
			line: "the metrics server listens on --bind-address=0.0.0.0:8080",
			want: false,
		},
		{
			name: "one stale reference among others",
			line: "kube v1.35.0 and api v1.0.0 but the chart is v0.2.0",
			want: true,
		},
		{
			name: "no version at all",
			line: "the driver allocates CPUs",
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := namesOlderRelease(tc.line, target); got != tc.want {
				t.Fatalf("namesOlderRelease(%q) = %v, want %v", tc.line, got, tc.want)
			}
		})
	}
}

func TestExempt(t *testing.T) {
	tests := []struct {
		name string
		path string
		line string
		want bool
	}{
		{
			name: "whole file",
			path: "docs/user/upgrade.md",
			line: "## Upgrade from 0.3.0 to 0.4.0",
			want: true,
		},
		{
			name: "matching text",
			path: "docs/dev/testing.md",
			line: "When deploying versions starting with 0.3.0 (included), use the migration path.",
			want: true,
		},
		{
			name: "text does not match",
			path: "docs/dev/testing.md",
			line: "Run the unit tests with make test-unit.",
			want: false,
		},
		{
			name: "matching text in a partially exempt file",
			path: "docs/user/configuration.md",
			line: "The flag is newer than the chart's `appVersion`, which still selects the v0.2.0 image.",
			want: true,
		},
		{
			name: "same text in another file",
			path: "docs/user/quickstart.md",
			line: "The flag is newer than the chart's `appVersion`, which still selects the v0.2.0 image.",
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := exempt(tc.path, tc.line); got != tc.want {
				t.Fatalf("exempt(%q, %q) = %v, want %v", tc.path, tc.line, got, tc.want)
			}
		})
	}
}

func TestDocumentFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "README.md")
	writeFile(t, root, "docs/toplevel.md")
	writeFile(t, root, "docs/user/deep.md")
	writeFile(t, root, "docs/notes.txt")
	writeFile(t, root, "deployment/helm/chart/README.md.gotmpl")

	git(t, root, "init", "-q")
	git(t, root, "add", "-A")

	pathspecs := []string{
		":(glob)docs/**/*.md",
		":(glob)README.md",
		":(glob)deployment/helm/*/README.md.gotmpl",
	}
	files, err := documentFiles(root, pathspecs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	want := []string{
		"README.md",
		"deployment/helm/chart/README.md.gotmpl",
		// The glob magic is what makes this one match: a plain pathspec
		// would skip the markdown file sitting directly under docs/.
		"docs/toplevel.md",
		"docs/user/deep.md",
	}
	got := append([]string(nil), files...)
	sort.Strings(got)
	if !slices.Equal(got, want) {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func writeFile(t *testing.T, root, path string) {
	t.Helper()
	full := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if err := os.WriteFile(full, []byte("placeholder\n"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	//nolint:gosec // the arguments are literals from this file, not user input
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}
