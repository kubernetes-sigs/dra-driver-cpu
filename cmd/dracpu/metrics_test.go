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

package main

import (
	"bytes"
	"encoding/json"
	"testing"

	cpumetrics "github.com/kubernetes-sigs/dra-driver-cpu/pkg/metrics"
	"github.com/stretchr/testify/require"
)

func TestPrintMetricsMetadata(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, printMetricsMetadata(&buf))

	var descriptors []cpumetrics.Descriptor
	require.NoError(t, json.Unmarshal(buf.Bytes(), &descriptors))
	require.Equal(t, cpumetrics.Descriptors(), descriptors)
	require.Contains(t, buf.String(), "dra_cpu_allocated_cpus")
	require.NotContains(t, buf.String(), "go_gc_duration_seconds")
}
