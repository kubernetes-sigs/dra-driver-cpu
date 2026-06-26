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

package metrics

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestDescriptors(t *testing.T) {
	descriptors := Descriptors()
	require.Len(t, descriptors, 8)

	names := make([]string, 0, len(descriptors))
	for _, desc := range descriptors {
		names = append(names, desc.Name)
		require.Equal(t, "ALPHA", desc.Stability)
		require.NotEmpty(t, desc.Help)
		require.NotNil(t, desc.Labels)
	}

	require.Equal(t, []string{
		"dra_cpu_allocated_cpus",
		"dra_cpu_available_cpus",
		"dra_cpu_reserved_cpus",
		"dra_cpu_resource_claims_active",
		"dra_cpu_prepare_claims_total",
		"dra_cpu_unprepare_claims_total",
		"dra_cpu_prepare_claim_duration_seconds",
		"dra_cpu_claim_allocated_cpus",
	}, names)
	require.Equal(t, []string{"result"}, descriptors[4].Labels)
	require.Equal(t, []string{"result"}, descriptors[5].Labels)
}

func TestWriteJSON(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, WriteJSON(&buf))
	require.True(t, bytes.HasSuffix(buf.Bytes(), []byte("\n")))

	var descriptors []Descriptor
	require.NoError(t, json.Unmarshal(buf.Bytes(), &descriptors))
	require.Equal(t, Descriptors(), descriptors)
}

func TestMetricsRecordsCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)

	m.SetAllocationState(AllocationState{
		AllocatedCPUs:        2,
		AvailableCPUs:        6,
		ReservedCPUs:         1,
		ActiveResourceClaims: 1,
	})
	m.RecordPrepare(ResultSuccess, 150*time.Millisecond)
	m.RecordPrepare(ResultError, 250*time.Millisecond)
	m.RecordUnprepare(ResultSuccess)
	m.RecordClaimAllocatedCPUs(2)

	require.InDelta(t, 2, testutil.ToFloat64(m.allocatedCPUs), 0.01)
	require.InDelta(t, 6, testutil.ToFloat64(m.availableCPUs), 0.01)
	require.InDelta(t, 1, testutil.ToFloat64(m.reservedCPUs), 0.01)
	require.InDelta(t, 1, testutil.ToFloat64(m.activeResourceClaims), 0.01)
	require.InDelta(t, 1, testutil.ToFloat64(m.prepareClaims.WithLabelValues(ResultSuccess)), 0.01)
	require.InDelta(t, 1, testutil.ToFloat64(m.prepareClaims.WithLabelValues(ResultError)), 0.01)
	require.InDelta(t, 1, testutil.ToFloat64(m.unprepareClaims.WithLabelValues(ResultSuccess)), 0.01)

	families, err := reg.Gather()
	require.NoError(t, err)
	require.NotEmpty(t, families)
	require.Equal(t, 1, testutil.CollectAndCount(m.prepareClaimDuration))
	require.Equal(t, 1, testutil.CollectAndCount(m.claimAllocatedCPUs))
}

func TestNoopRecorder(t *testing.T) {
	recorder := Noop()

	require.NotPanics(t, func() {
		recorder.SetAllocationState(AllocationState{})
		recorder.RecordPrepare(ResultSuccess, time.Second)
		recorder.RecordUnprepare(ResultError)
		recorder.RecordClaimAllocatedCPUs(4)
	})
}
