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

package api

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"k8s.io/utils/cpuset"
)

func TestParseOpaqueConfig(t *testing.T) {
	testCases := []struct {
		name          string
		rawJSON       string
		expected      cpuset.CPUSet
		expectedError string
	}{
		{
			name:     "valid config",
			rawJSON:  `{"apiVersion": "v1alpha1", "cpuConfig": {"cpuset": "2-5"}}`,
			expected: cpuset.New(2, 3, 4, 5),
		},
		{
			name:          "invalid - empty cpuset",
			rawJSON:       `{"apiVersion": "v1alpha1", "cpuConfig": {"cpuset": ""}}`,
			expectedError: "opaque config: cpuConfig.cpuset is empty or missing",
		},
		{
			name:          "invalid - invalid cpuset format",
			rawJSON:       `{"apiVersion": "v1alpha1", "cpuConfig": {"cpuset": "abc"}}`,
			expectedError: "opaque config: failed to parse cpuset \"abc\"",
		},
		{
			name:          "invalid - unsupported version",
			rawJSON:       `{"apiVersion": "v1beta1", "cpuConfig": {"cpuset": "0-3"}}`,
			expectedError: "unsupported opaque config apiVersion: \"v1beta1\"",
		},
		{
			name:          "invalid - missing version",
			rawJSON:       `{"cpuConfig": {"cpuset": "0-3"}}`,
			expectedError: "unsupported opaque config apiVersion: \"\"",
		},
		{
			name:          "invalid - malformed json",
			rawJSON:       `{"apiVersion": "v1alpha1", "cpuConfig":`,
			expectedError: "failed to unmarshal opaque config",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := ParseOpaqueConfig([]byte(tc.rawJSON))
			if tc.expectedError != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tc.expectedError)
				assert.True(t, result.IsEmpty())
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tc.expected, result)
			}
		})
	}
}
