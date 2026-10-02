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

package release

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestPublishRelease(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "cloudbuild.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Steps []struct {
			Entrypoint string
			Env        []string
		}
	}
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Steps) != 1 {
		t.Fatalf("expected one publishing step, got %d", len(config.Steps))
	}
	step := config.Steps[0]
	if _, err := exec.LookPath("bash"); err != nil {
		t.Fatal(err)
	}
	git, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name      string
		prowRef   string
		nativeTag string
		imageTag  string
		failAt    string
		wantTag   string
		wantChart bool
	}{
		{name: "prow tag", prowRef: "v0.3.0", wantTag: "v0.3.0", wantChart: true},
		{name: "prow prerelease", prowRef: "v0.3.0-rc.1", wantTag: "v0.3.0-rc.1", wantChart: true},
		{name: "main on tagged commit", prowRef: "main"},
		{name: "release branch on tagged commit", prowRef: "release-v0.3"},
		{name: "non release ref", prowRef: "v0.3"},
		{name: "invalid version", prowRef: "v01.2.3"},
		{name: "native cloud build tag", nativeTag: "v0.3.0", wantTag: "v0.3.0", wantChart: true},
		{name: "prow branch takes precedence", prowRef: "main", nativeTag: "v0.3.0"},
		{name: "local tagged checkout"},
		{name: "explicit image tag is not a release event", imageTag: "v1.2.3", wantTag: "v1.2.3"},
		{name: "trigger tag takes precedence", prowRef: "v0.3.0", imageTag: "v1.2.3", wantTag: "v0.3.0", wantChart: true},
		{name: "image push fails", prowRef: "v0.3.0", failAt: "push-image", wantTag: "v0.3.0"},
		{name: "image inspection fails", prowRef: "v0.3.0", failAt: "inspect", wantTag: "v0.3.0"},
		{name: "chart push fails", prowRef: "v0.3.0", failAt: "helm-push", wantTag: "v0.3.0", wantChart: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, args := range [][]string{
				{"init", "-q"},
				{"-c", "user.name=Release Test", "-c", "user.email=release@example.invalid", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null", "commit", "--allow-empty", "-qm", "fixture"},
				{"-c", "tag.gpgSign=false", "tag", "v0.3.0"},
				{"-c", "tag.gpgSign=false", "tag", "v0.3.0-rc.1"},
			} {
				cmd := exec.Command(git, args...)
				cmd.Dir = dir
				if output, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, output)
				}
			}
			// Only Git is real. Publishing commands record their arguments and cannot push.
			bin := filepath.Join(dir, "bin")
			if err := os.Mkdir(bin, 0755); err != nil {
				t.Fatal(err)
			}
			for name, script := range map[string]string{
				"make": `#!/bin/sh
printf 'make %s\n' "$*" >> "$PUBLISH_TEST_TRACE"
case "$1" in push-image|helm-push) ;; *) exit 99 ;; esac
if [ "$1" = "$PUBLISH_TEST_FAIL_AT" ]; then exit 42; fi
`,
				"docker": `#!/bin/sh
printf 'docker %s\n' "$*" >> "$PUBLISH_TEST_TRACE"
if [ "$1 $2 $3" != 'buildx imagetools inspect' ]; then exit 99; fi
if [ "$PUBLISH_TEST_FAIL_AT" = inspect ]; then exit 42; fi
`,
			} {
				// #nosec G306 -- These temporary test commands must be executable.
				if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
			}
			trace := filepath.Join(dir, "calls")
			// #nosec G204 -- Run the repository's entrypoint with fake publishing tools.
			cmd := exec.Command("bash", filepath.Join(root, step.Entrypoint))
			cmd.Dir = dir
			cmd.Env = []string{
				"PATH=" + bin + string(os.PathListSeparator) + os.Getenv("PATH"),
				"HOME=" + dir,
				"PUBLISH_TEST_TRACE=" + trace,
				"PUBLISH_TEST_FAIL_AT=" + tc.failAt,
			}
			// Match the substitutions sent by test-infra's image-builder. A storage
			// source has no built-in TAG_NAME unless the caller supplies it explicitly.
			subs := map[string]string{"_PULL_BASE_REF": tc.prowRef, "TAG_NAME": tc.nativeTag}
			for _, env := range step.Env {
				if strings.HasPrefix(env, "IMG_TAG=") && tc.imageTag != "" {
					continue
				}
				cmd.Env = append(cmd.Env, os.Expand(env, func(key string) string { return subs[key] }))
			}
			if tc.imageTag != "" {
				cmd.Env = append(cmd.Env, "IMG_TAG="+tc.imageTag)
			}
			output, runErr := cmd.CombinedOutput()
			if (runErr != nil) != (tc.failAt != "") {
				t.Fatalf("unexpected publishing result: %v\n%s", runErr, output)
			}
			calls, err := os.ReadFile(trace)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(calls)), "\n")
			wantCalls := 2
			if tc.failAt == "push-image" {
				wantCalls = 1
			}
			if tc.wantChart {
				wantCalls = 3
			}
			if len(lines) != wantCalls || !strings.HasPrefix(lines[0], "make push-image ") {
				t.Fatalf("unexpected publish calls:\n%s\n%s", calls, output)
			}
			if tc.wantTag != "" && !strings.Contains(lines[0], " TAG="+tc.wantTag+" ") {
				t.Errorf("image tag: want %s, got %s", tc.wantTag, lines[0])
			}
			if len(lines) > 1 && tc.wantTag != "" {
				want := "docker buildx imagetools inspect us-central1-docker.pkg.dev/k8s-staging-images/dra-driver-cpu/dra-driver-cpu:" + tc.wantTag
				if lines[1] != want {
					t.Errorf("image inspection:\nwant %s\ngot  %s", want, lines[1])
				}
			}
			if got := strings.Contains(string(calls), "make helm-push "); got != tc.wantChart {
				t.Errorf("chart publishing: want %v, got %v\n%s", tc.wantChart, got, output)
			}
			if tc.wantChart {
				want := fmt.Sprintf("make helm-push CHART_REGISTRY=us-central1-docker.pkg.dev/k8s-staging-images/dra-driver-cpu/charts CHART_VERSION=%s TAG=%s", strings.TrimPrefix(tc.wantTag, "v"), tc.wantTag)
				if lines[2] != want {
					t.Errorf("chart arguments:\nwant %s\ngot  %s", want, lines[2])
				}
			}
		})
	}
}
