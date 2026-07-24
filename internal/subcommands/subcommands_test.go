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

package subcommands

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"

	cpumetrics "github.com/kubernetes-sigs/dra-driver-cpu/pkg/metrics"
)

func TestRunIntrospectMetrics(t *testing.T) {
	var stdout bytes.Buffer

	err := Run([]string{"introspect", "metrics"}, Options{
		Stdout: &stdout,
		Stderr: io.Discard,
	})
	if err != nil {
		t.Fatalf("Run(introspect metrics) error = %v", err)
	}

	var descriptors []cpumetrics.Descriptor
	if err := json.Unmarshal(stdout.Bytes(), &descriptors); err != nil {
		t.Fatalf("metrics output is not valid JSON: %v", err)
	}
	if len(descriptors) == 0 {
		t.Fatal("metrics output is empty")
	}
	if findDescriptorByName(descriptors, "dra_cpu_allocated_cpus") == nil {
		t.Fatal("metrics output does not include dra_cpu_allocated_cpus")
	}
}

func TestRunIntrospectRequiresNestedSubcommand(t *testing.T) {
	err := runIntrospect(nil, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("runIntrospect() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "requires a subcommand") {
		t.Fatalf("runIntrospect() error = %v, want missing subcommand error", err)
	}
}

func TestRunMetricsRejectsPositionalArgs(t *testing.T) {
	err := runMetrics([]string{"unexpected"}, io.Discard, io.Discard)
	if err == nil {
		t.Fatal("runMetrics() succeeded, want error")
	}
	if !strings.Contains(err.Error(), "does not accept positional arguments") {
		t.Fatalf("runMetrics() error = %v, want positional arguments error", err)
	}
}

func TestRunRejectsUnknownSubcommand(t *testing.T) {
	err := Run([]string{"unknown"}, Options{
		Stdout: io.Discard,
		Stderr: io.Discard,
	})
	if err == nil {
		t.Fatal("Run() succeeded, want error")
	}
	if !strings.Contains(err.Error(), `unknown subcommand "unknown"`) {
		t.Fatalf("Run() error = %v, want unknown subcommand error", err)
	}
}

func findDescriptorByName(descriptors []cpumetrics.Descriptor, name string) *cpumetrics.Descriptor {
	for idx := range descriptors {
		desc := &descriptors[idx]
		if desc.Name == name {
			return desc
		}
	}
	return nil
}
