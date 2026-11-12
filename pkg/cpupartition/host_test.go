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

package cpupartition

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func withRelease(t *testing.T, release string) {
	t.Helper()
	orig := osRelease
	osRelease = func() (string, error) { return release, nil }
	t.Cleanup(func() { osRelease = orig })
}

func TestCheckHostKernel(t *testing.T) {
	for release, ok := range map[string]bool{
		"7.0.0":                    true,
		"7.0-rc1":                  true,
		"7.4.0-rc0":                true,
		"8.1.2":                    true,
		"6.19.10-300.fc44.aarch64": false,
		"6.7.0":                    false,
	} {
		t.Run(release, func(t *testing.T) {
			withRelease(t, release)
			m, _ := newManager(t, ModeRoot)
			err := m.CheckHost()
			if ok {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "upgrade the kernel")
			}
		})
	}
	for _, release := range []string{"", "7", "x.1", "7.y"} {
		withRelease(t, release)
		m, _ := newManager(t, ModeRoot)
		require.ErrorContains(t, m.CheckHost(), "parse kernel release", release)
	}
}

func TestCheckHostForeignPartition(t *testing.T) {
	withRelease(t, "7.0.0")
	m, root := newManager(t, ModeRoot)
	// A missing scope holds no foreign partition yet.
	require.NoError(t, m.CheckHost())

	create(t, root, "pods/pod1/ctr", "1")
	require.NoError(t, m.CheckHost())

	set(t, root, "pods", filePartition, "root")
	require.ErrorContains(t, m.CheckHost(), "CPUSetPartition=")

	// Partitions below the scope are the Manager's own.
	set(t, root, "pods", filePartition, modeMember)
	set(t, root, "pods/pod1/ctr", filePartition, "root")
	require.NoError(t, m.CheckHost())
}
