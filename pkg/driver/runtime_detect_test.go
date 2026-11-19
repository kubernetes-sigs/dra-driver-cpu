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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRuntimeSupportsUnsolicitedUpdate(t *testing.T) {
	tests := []struct {
		name            string
		runtimeName     string
		runtimeVersion  string
		expectSupported bool
		expectReason    string
	}{
		// containerd cases
		{
			name:            "containerd 2.4.0 - minimum supported",
			runtimeName:     "containerd",
			runtimeVersion:  "2.4.0",
			expectSupported: true,
			expectReason:    "detected containerd 2.4.0",
		},
		{
			name:            "containerd 2.4.1 - supported",
			runtimeName:     "containerd",
			runtimeVersion:  "2.4.1",
			expectSupported: true,
			expectReason:    "detected containerd 2.4.1",
		},
		{
			name:            "containerd 2.5.0 - supported",
			runtimeName:     "containerd",
			runtimeVersion:  "2.5.0",
			expectSupported: true,
			expectReason:    "detected containerd 2.5.0",
		},
		{
			name:            "containerd 2.3.7 - too old",
			runtimeName:     "containerd",
			runtimeVersion:  "2.3.7",
			expectSupported: false,
			expectReason:    "containerd 2.3.7 predates nri v0.12.3 (needs 2.4.0+)",
		},
		{
			name:            "containerd 2.3.4 - too old",
			runtimeName:     "containerd",
			runtimeVersion:  "2.3.4",
			expectSupported: false,
			expectReason:    "containerd 2.3.4 predates nri v0.12.3 (needs 2.4.0+)",
		},
		{
			name:            "containerd 1.7.0 - too old",
			runtimeName:     "containerd",
			runtimeVersion:  "1.7.0",
			expectSupported: false,
			expectReason:    "containerd 1.7.0 predates nri v0.12.3 (needs 2.4.0+)",
		},
		{
			name:            "containerd case insensitive",
			runtimeName:     "Containerd",
			runtimeVersion:  "2.4.0",
			expectSupported: true,
			expectReason:    "detected containerd 2.4.0",
		},

		// cri-o cases
		{
			name:            "cri-o 1.37.0 - minimum supported",
			runtimeName:     "cri-o",
			runtimeVersion:  "1.37.0",
			expectSupported: true,
			expectReason:    "detected cri-o 1.37.0",
		},
		{
			name:            "cri-o 1.37.1 - supported",
			runtimeName:     "cri-o",
			runtimeVersion:  "1.37.1",
			expectSupported: true,
			expectReason:    "detected cri-o 1.37.1",
		},
		{
			name:            "cri-o 1.38.0 - supported",
			runtimeName:     "cri-o",
			runtimeVersion:  "1.38.0",
			expectSupported: true,
			expectReason:    "detected cri-o 1.38.0",
		},
		{
			name:            "cri-o 1.36.5 - too old",
			runtimeName:     "cri-o",
			runtimeVersion:  "1.36.5",
			expectSupported: false,
			expectReason:    "cri-o 1.36.5 predates nri v0.12.3 (needs 1.37.0+)",
		},
		{
			name:            "cri-o 1.30.0 - too old",
			runtimeName:     "cri-o",
			runtimeVersion:  "1.30.0",
			expectSupported: false,
			expectReason:    "cri-o 1.30.0 predates nri v0.12.3 (needs 1.37.0+)",
		},
		{
			name:            "cri-o case insensitive",
			runtimeName:     "CRI-O",
			runtimeVersion:  "1.37.0",
			expectSupported: true,
			expectReason:    "detected cri-o 1.37.0",
		},

		// Unknown/vendor runtimes
		{
			name:            "k3s - unknown",
			runtimeName:     "k3s",
			runtimeVersion:  "1.32.1+k3s1",
			expectSupported: false,
			expectReason:    "unknown runtime k3s/1.32.1+k3s1",
		},
		{
			name:            "microk8s - unknown",
			runtimeName:     "microk8s",
			runtimeVersion:  "2.3.4",
			expectSupported: false,
			expectReason:    "unknown runtime microk8s/2.3.4",
		},
		{
			name:            "docker - unknown",
			runtimeName:     "docker",
			runtimeVersion:  "24.0.0",
			expectSupported: false,
			expectReason:    "unknown runtime docker/24.0.0",
		},

		// Invalid versions
		{
			name:            "invalid version string",
			runtimeName:     "containerd",
			runtimeVersion:  "not-a-version",
			expectSupported: false,
			expectReason:    `unknown runtime containerd (invalid version "not-a-version")`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			supported, reason := runtimeSupportsUnsolicitedUpdate(tt.runtimeName, tt.runtimeVersion)
			require.Equal(t, tt.expectSupported, supported, "support mismatch")
			require.Equal(t, tt.expectReason, reason, "reason mismatch")
		})
	}
}
