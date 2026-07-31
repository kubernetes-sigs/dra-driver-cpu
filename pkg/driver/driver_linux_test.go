//go:build linux

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
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Pins unixPathMax to what binding a socket actually accepts, rather than to a
// number copied from a header. Linux only: sun_path is not a fixed size across
// Unix, and 104 elsewhere would fail the at-the-limit case.
func TestSocketPathLimitMatchesTheListener(t *testing.T) {
	dir := t.TempDir()
	// A temporary directory long enough to leave no room would otherwise ask for
	// a negative repeat count below.
	if len(dir)+1 >= unixPathMax {
		t.Skipf("temporary directory %q leaves no room for a socket name", dir)
	}
	for _, tc := range []struct {
		name      string
		length    int
		wantBound bool
	}{
		{"at the limit", unixPathMax, true},
		{"one byte over", unixPathMax + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := strings.Repeat("s", tc.length-len(dir)-1)
			path := filepath.Join(dir, name)
			require.Len(t, path, tc.length, "test setup")

			l, err := net.Listen("unix", path)
			if tc.wantBound {
				require.NoError(t, err, "unixPathMax is below what a socket accepts here")
				require.NoError(t, l.Close())
				return
			}
			require.Error(t, err, "unixPathMax is above what a socket accepts here")
		})
	}
}
