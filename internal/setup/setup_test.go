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

package setup_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/setup"
	"github.com/pelletier/go-toml/v2"
	"github.com/stretchr/testify/require"
)

func TestContainerdConfig(t *testing.T) {
	for _, tc := range []struct{ name, version, key string }{
		{"v2", "2", "io.containerd.grpc.v1.cri"}, {"v3", "3", "io.containerd.cri.v1.runtime"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := `version = ` + tc.version + `
root = "/var/lib/containerd"
[plugins."` + tc.key + `"]
disable_hugetlb_controller = true
[plugins."` + tc.key + `".containerd.runtimes.runc.options]
SystemdCgroup = true
[plugins."io.containerd.cri.v1.images"]
snapshotter = "overlayfs"
`
			var out bytes.Buffer
			require.NoError(t, setup.ConfigStream(strings.NewReader(input), &out))
			require.NoError(t, setup.CheckRuntime(bytes.NewReader(out.Bytes())))
			var got map[string]any
			require.NoError(t, toml.Unmarshal(out.Bytes(), &got))
			require.Equal(t, "/var/lib/containerd", got["root"])
			plugins := got["plugins"].(map[string]any)
			runtime := plugins[tc.key].(map[string]any)
			require.Equal(t, true, runtime["enable_cdi"])
			require.Equal(t, false, runtime["disable_hugetlb_controller"])
			require.Equal(t, false, runtime["tolerate_missing_hugetlb_controller"])
			options := runtime["containerd"].(map[string]any)["runtimes"].(map[string]any)["runc"].(map[string]any)["options"].(map[string]any)
			require.Equal(t, true, options["SystemdCgroup"])
			require.Equal(t, "overlayfs", plugins["io.containerd.cri.v1.images"].(map[string]any)["snapshotter"])
			var second bytes.Buffer
			require.NoError(t, setup.ConfigStream(bytes.NewReader(out.Bytes()), &second))
			require.Equal(t, out.String(), second.String())
		})
	}
	for _, input := range []string{`version=1`, "version=3\n[plugins]", `invalid toml = ???`} {
		var out bytes.Buffer
		require.Error(t, setup.ConfigStream(strings.NewReader(input), &out))
		require.Empty(t, out.String())
		require.Error(t, setup.CheckRuntime(strings.NewReader(input)))
	}
}

func TestParentBudget(t *testing.T) {
	for _, tc := range []struct {
		value string
		bad   bool
	}{{"max", false}, {"1073741824", false}, {"0", true}, {"bad", true}, {"-1", true}} {
		t.Run(tc.value, func(t *testing.T) {
			err := setup.CheckParentBudget(tc.value, 1<<30, "hugetlb.1GB.max")
			if tc.bad {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestKindEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name, controllers, parent, options string
		bad                                bool
	}{
		{"ready without 1 GiB support", "cpuset memory hugetlb", "max", "rw", false},
		{"zero 1 GiB parent", "cpuset memory hugetlb", "max", "rw", false},
		{"missing delegation", "memory hugetlb", "max", "rw", true},
		{"parent zero", "cpuset memory hugetlb", "0", "rw", true},
		{"combined accounting", "cpuset memory hugetlb", "max", "rw,memory_hugetlb_accounting", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			files := map[string]string{
				"proc/thread-self/mountinfo":       "1 0 0:1 / /sys/fs/cgroup rw - cgroup2 cgroup2 " + tc.options,
				"proc/swaps":                       "Filename Type Size Used Priority\n",
				"sys/fs/cgroup/cgroup.controllers": tc.controllers,
				"sys/fs/cgroup/hugetlb.2MB.max":    tc.parent,
			}
			if tc.name == "zero 1 GiB parent" {
				files["sys/fs/cgroup/hugetlb.1GB.max"] = "0"
			}
			for name, data := range files {
				path := filepath.Join(root, name)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0750))
				require.NoError(t, os.WriteFile(path, []byte(data), 0600))
			}
			err := setup.CheckEnvironment(testr.New(t), root, true, true)
			if tc.bad {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestBorrowedHugepageProvisioning(t *testing.T) {
	root := t.TempDir()
	for name, data := range map[string]string{"sys/devices/system/node/online": "0", "sys/devices/system/node/node0/hugepages/hugepages-2048kB/nr_hugepages": "0"} {
		path := filepath.Join(root, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0750))
		require.NoError(t, os.WriteFile(path, []byte(data), 0600))
	}
	config, err := setup.ReadConfigurationFrom(strings.NewReader("kind: HugePageProvision\nmetadata:\n  name: test\nspec:\n  pages:\n  - size: 2M\n    count: 2\n"))
	require.NoError(t, err)
	require.NoError(t, setup.ProvisionHugepages(testr.New(t), config, root))
	data, err := os.ReadFile(filepath.Join(root, "sys/devices/system/node/node0/hugepages/hugepages-2048kB/nr_hugepages"))
	require.NoError(t, err)
	require.Equal(t, "2", string(data))
	config.Spec.Pages[0].Count = -1
	require.ErrorContains(t, setup.ProvisionHugepages(testr.New(t), config, root), "nonnegative")
}

func TestRuntimeReadiness(t *testing.T) {
	require.NoError(t, setup.CheckCRI(strings.NewReader(`{"status":{"conditions":[{"type":"RuntimeReady","status":true}]}}`)))
	for _, data := range []string{`{}`, `{"status":{"conditions":[{"type":"RuntimeReady","status":false}]}}`, `bad`} {
		require.Error(t, setup.CheckCRI(strings.NewReader(data)))
	}
}

func TestHostEnvironmentWithOnly2MPages(t *testing.T) {
	for _, tc := range []struct {
		name, count string
		bad         bool
	}{{"2 MiB only", "4", false}, {"not provisioned", "0", true}} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for name, data := range map[string]string{
				"proc/thread-self/mountinfo":       "1 0 0:1 / /sys/fs/cgroup rw - cgroup2 cgroup2 rw",
				"proc/swaps":                       "Filename Type Size Used Priority\n",
				"sys/fs/cgroup/cgroup.controllers": "cpuset memory hugetlb",
				"sys/devices/system/node/node0/hugepages/hugepages-2048kB/nr_hugepages": tc.count,
			} {
				path := filepath.Join(root, name)
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0750))
				require.NoError(t, os.WriteFile(path, []byte(data), 0600))
			}
			err := setup.CheckEnvironment(testr.New(t), root, true, false)
			if tc.bad {
				require.ErrorContains(t, err, "2 MiB")
			} else {
				require.NoError(t, err)
			}
		})
	}
}
