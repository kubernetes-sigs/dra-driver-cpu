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

	v1 "k8s.io/api/core/v1"
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
	t.Setenv("DRACPU_E2E_RESERVED_CPUS", "1-2,5")

	got := parseOptionalCPUSetEnv("DRACPU_E2E_RESERVED_CPUS")
	want := cpuset.New(1, 2, 5)
	if !got.Equals(want) {
		t.Fatalf("expected %s, got %s", want.String(), got.String())
	}
}

func TestDriverConfigFromContainerParsesRelevantArgs(t *testing.T) {
	container := &v1.Container{
		Args: []string{
			"--reserved-cpus=1-2",
			"--cpu-device-mode=grouped",
			"--group-by=socket",
			"--something-else=ignored",
		},
	}

	got := driverConfigFromContainer(container)
	if !got.ReservedCPUs.Equals(cpuset.New(1, 2)) {
		t.Fatalf("expected reserved cpus 1-2, got %s", got.ReservedCPUs.String())
	}
	if got.CPUDeviceMode != "grouped" {
		t.Fatalf("expected cpu device mode grouped, got %q", got.CPUDeviceMode)
	}
	if got.GroupBy != "socket" {
		t.Fatalf("expected group-by socket, got %q", got.GroupBy)
	}
}
