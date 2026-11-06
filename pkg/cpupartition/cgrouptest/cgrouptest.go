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

// Package cgrouptest fakes cpuset cgroup files with regular files. The kernel
// never updates them, so tests set the effective files themselves.
package cgrouptest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Create makes dir and its ancestors below root as member cgroups with the
// cpuset controller. The leaf gets cpus as its runtime and effective mask.
func Create(t testing.TB, root, dir, cpus string) {
	t.Helper()
	parts := strings.Split(dir, "/")
	for i := range parts {
		d := filepath.Join(root, filepath.Join(parts[:i+1]...))
		if err := os.MkdirAll(d, 0750); err != nil {
			t.Fatal(err)
		}
		files := map[string]string{"cpuset.cpus.exclusive": "", "cpuset.cpus.partition": "member"}
		if i == len(parts)-1 && cpus != "" {
			for _, name := range []string{"cpuset.cpus", "cpuset.cpus.effective", "cpuset.cpus.exclusive.effective"} {
				files[name] = cpus
			}
		}
		for name, value := range files {
			if _, err := os.Stat(filepath.Join(d, name)); os.IsNotExist(err) {
				Write(t, root, filepath.Join(parts[:i+1]...), name, value)
			}
		}
	}
}

//nolint:gosec // Test helper; root is a temporary directory or the cgroup root of an opt-in test.
func Write(t testing.TB, root, dir, name, value string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, dir, name), []byte(value+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
}

//nolint:gosec // See Write.
func Read(t testing.TB, root, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}
