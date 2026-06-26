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
	require.InDelta(t, 1, testutil.ToFloat64(m.prepareClaims.WithLabelValues(ResultSuccess.String())), 0.01)
	require.InDelta(t, 1, testutil.ToFloat64(m.prepareClaims.WithLabelValues(ResultError.String())), 0.01)
	require.InDelta(t, 1, testutil.ToFloat64(m.unprepareClaims.WithLabelValues(ResultSuccess.String())), 0.01)

	families, err := reg.Gather()
	require.NoError(t, err)
	require.NotEmpty(t, families)
	require.Equal(t, 1, testutil.CollectAndCount(m.prepareClaimDuration))
	require.Equal(t, 1, testutil.CollectAndCount(m.claimAllocatedCPUs))
}

func TestDescriptorsMatchRegisteredCollectors(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)
	m.SetAllocationState(AllocationState{})
	m.RecordPrepare(ResultSuccess, time.Second)
	m.RecordPrepare(ResultError, time.Second)
	m.RecordUnprepare(ResultSuccess)
	m.RecordUnprepare(ResultError)
	m.RecordClaimAllocatedCPUs(1)

	families, err := reg.Gather()
	require.NoError(t, err)
	gotNames := make([]string, 0, len(families))
	for _, family := range families {
		gotNames = append(gotNames, family.GetName())
	}

	wantNames := make([]string, 0, len(Descriptors()))
	for _, descriptor := range Descriptors() {
		wantNames = append(wantNames, descriptor.Name)
	}
	require.ElementsMatch(t, wantNames, gotNames)
}

func TestMetricsRejectsUnboundedResultLabels(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := New(reg)

	m.RecordPrepare(Result("timeout"), time.Second)
	m.RecordUnprepare(Result("permission-denied"))

	require.InDelta(t, 1, testutil.ToFloat64(m.prepareClaims.WithLabelValues(ResultError.String())), 0.01)
	require.InDelta(t, 1, testutil.ToFloat64(m.unprepareClaims.WithLabelValues(ResultError.String())), 0.01)
	requireMetricLabelValueAbsent(t, reg, "dra_cpu_prepare_claims_total", "result", "timeout")
	requireMetricLabelValueAbsent(t, reg, "dra_cpu_unprepare_claims_total", "result", "permission-denied")
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

func requireMetricLabelValueAbsent(t *testing.T, reg *prometheus.Registry, metricName, labelName, labelValue string) {
	t.Helper()
	families, err := reg.Gather()
	require.NoError(t, err)
	for _, family := range families {
		if family.GetName() != metricName {
			continue
		}
		for _, metric := range family.Metric {
			for _, label := range metric.Label {
				require.False(t, label.GetName() == labelName && label.GetValue() == labelValue,
					"metric %s has unexpected label %s=%q", metricName, labelName, labelValue)
			}
		}
	}
}
