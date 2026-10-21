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

package driverconfig_test

import (
	"os"
	"strings"
	"testing"

	"github.com/kubernetes-sigs/dra-driver-cpu/internal/driverconfig"
)

func TestResolveNodeName(t *testing.T) {
	host, _ := os.Hostname()
	tests := []struct {
		name     string
		override string
		env      string
		want     string
	}{
		{name: "override wins over env", override: "Override-Node", env: "env-node", want: "override-node"},
		{name: "env used when override empty", env: "Env-Node", want: "env-node"},
		{name: "falls back to os hostname", want: strings.ToLower(strings.TrimSpace(host))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NODE_NAME", tc.env)
			got, err := driverconfig.ResolveNodeName(tc.override)
			if err != nil {
				t.Fatalf("ResolveNodeName() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("ResolveNodeName() = %q, want %q", got, tc.want)
			}
		})
	}
}
