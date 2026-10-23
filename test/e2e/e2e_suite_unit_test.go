/*
Copyright 2026 The Kubernetes Authors.

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

package e2e

import (
	"testing"

	"github.com/onsi/gomega"
	"k8s.io/utils/cpuset"
)

func TestParseOptionalCPUSetEnvReturnsEmptyWhenUnset(t *testing.T) {
	t.Setenv("DRACPU_E2E_RESERVED_CPUS", "")

	got := parseOptionalCPUSetEnv("DRACPU_E2E_RESERVED_CPUS")
	if !got.Equals(cpuset.New()) {
		t.Fatalf("expected empty cpuset, got %s", got.String())
	}
}

func TestParseOptionalCPUSetEnvParsesValue(t *testing.T) {
	// parseOptionalCPUSetEnv asserts with gomega, which needs a fail handler outside Ginkgo.
	gomega.RegisterTestingT(t)
	t.Setenv("DRACPU_E2E_RESERVED_CPUS", "1-2,5")

	got := parseOptionalCPUSetEnv("DRACPU_E2E_RESERVED_CPUS")
	want := cpuset.New(1, 2, 5)
	if !got.Equals(want) {
		t.Fatalf("expected %s, got %s", want.String(), got.String())
	}
}
