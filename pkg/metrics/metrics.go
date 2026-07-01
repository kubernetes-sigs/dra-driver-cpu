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
	"encoding/json"
	"io"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

const stability = "ALPHA"

// Result is the bounded label domain for operation result metrics.
type Result string

const (
	ResultSuccess Result = "success"
	ResultError   Result = "error"
	ResultUnknown Result = "unknown"
)

func (r Result) String() string {
	switch r {
	case ResultSuccess:
		return string(ResultSuccess)
	case ResultError:
		return string(ResultError)
	case ResultUnknown:
		return string(ResultUnknown)
	default:
		return string(ResultUnknown)
	}
}

// Descriptor describes a custom driver metric for programmatic introspection.
type Descriptor struct {
	Name      string   `json:"name"`
	Type      string   `json:"type"`
	Stability string   `json:"stability"`
	Help      string   `json:"help"`
	Labels    []string `json:"labels"`
}

// AllocationState is the allocation state represented by CPU allocation gauges.
type AllocationState struct {
	AllocatedCPUs        int
	AvailableCPUs        int
	ReservedCPUs         int
	ActiveResourceClaims int
}

// Recorder records driver metrics.
type Recorder interface {
	SetAllocationState(AllocationState)
	RecordPrepare(result Result, duration time.Duration)
	RecordUnprepare(result Result)
	RecordClaimAllocatedCPUs(cpus int)
}

// Metrics owns all custom Prometheus collectors for the CPU driver.
type Metrics struct {
	allocatedCPUs        prometheus.Gauge
	availableCPUs        prometheus.Gauge
	reservedCPUs         prometheus.Gauge
	activeResourceClaims prometheus.Gauge
	prepareClaims        *prometheus.CounterVec
	unprepareClaims      *prometheus.CounterVec
	prepareClaimDuration prometheus.Histogram
	claimAllocatedCPUs   prometheus.Histogram
}

type metricKind string

const (
	metricGauge     metricKind = "gauge"
	metricCounter   metricKind = "counter"
	metricHistogram metricKind = "histogram"
)

type metricSpec struct {
	name    string
	kind    metricKind
	help    string
	labels  []string
	buckets []float64
}

var (
	allocatedCPUsSpec = metricSpec{
		name: "dra_cpu_allocated_cpus",
		kind: metricGauge,
		help: "Number of CPUs currently allocated to prepared resource claims.",
	}
	availableCPUsSpec = metricSpec{
		name: "dra_cpu_available_cpus",
		kind: metricGauge,
		help: "Number of CPUs available for allocation after reserved and active claim CPUs are excluded.",
	}
	reservedCPUsSpec = metricSpec{
		name: "dra_cpu_reserved_cpus",
		kind: metricGauge,
		help: "Number of CPUs reserved and excluded from DRA management.",
	}
	activeResourceClaimsSpec = metricSpec{
		name: "dra_cpu_resource_claims_active",
		kind: metricGauge,
		help: "Number of resource claims currently recorded as active by the allocation store.",
	}
	prepareClaimsSpec = metricSpec{
		name:   "dra_cpu_prepare_claims_total",
		kind:   metricCounter,
		help:   "Total number of per-claim PrepareResourceClaims results.",
		labels: []string{"result"},
	}
	unprepareClaimsSpec = metricSpec{
		name:   "dra_cpu_unprepare_claims_total",
		kind:   metricCounter,
		help:   "Total number of per-claim UnprepareResourceClaims results.",
		labels: []string{"result"},
	}
	prepareClaimDurationSpec = metricSpec{
		name:    "dra_cpu_prepare_claim_duration_seconds",
		kind:    metricHistogram,
		help:    "Duration of per-claim prepare operations in seconds.",
		buckets: prometheus.DefBuckets,
	}
	claimAllocatedCPUsSpec = metricSpec{
		name:    "dra_cpu_claim_allocated_cpus",
		kind:    metricHistogram,
		help:    "Number of CPUs allocated for each newly successful claim allocation.",
		buckets: []float64{1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024},
	}
)

var metricSpecs = []metricSpec{
	allocatedCPUsSpec,
	availableCPUsSpec,
	reservedCPUsSpec,
	activeResourceClaimsSpec,
	prepareClaimsSpec,
	unprepareClaimsSpec,
	prepareClaimDurationSpec,
	claimAllocatedCPUsSpec,
}

// Descriptors returns metadata for custom CPU driver metrics.
func Descriptors() []Descriptor {
	out := make([]Descriptor, len(metricSpecs))
	for i, spec := range metricSpecs {
		out[i] = Descriptor{
			Name:      spec.name,
			Type:      string(spec.kind),
			Stability: stability,
			Help:      spec.help,
			Labels:    append([]string{}, spec.labels...),
		}
	}
	return out
}

// WriteJSON writes custom metric metadata as deterministic JSON.
func WriteJSON(w io.Writer) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(Descriptors())
}

// New creates and registers the CPU driver custom metrics.
func New(reg prometheus.Registerer) *Metrics {
	if reg == nil {
		reg = prometheus.DefaultRegisterer
	}

	m := &Metrics{
		allocatedCPUs:        newGauge(allocatedCPUsSpec),
		availableCPUs:        newGauge(availableCPUsSpec),
		reservedCPUs:         newGauge(reservedCPUsSpec),
		activeResourceClaims: newGauge(activeResourceClaimsSpec),
		prepareClaims:        newCounterVec(prepareClaimsSpec),
		unprepareClaims:      newCounterVec(unprepareClaimsSpec),
		prepareClaimDuration: newHistogram(prepareClaimDurationSpec),
		claimAllocatedCPUs:   newHistogram(claimAllocatedCPUsSpec),
	}

	reg.MustRegister(
		m.allocatedCPUs,
		m.availableCPUs,
		m.reservedCPUs,
		m.activeResourceClaims,
		m.prepareClaims,
		m.unprepareClaims,
		m.prepareClaimDuration,
		m.claimAllocatedCPUs,
	)
	for _, result := range []Result{ResultSuccess, ResultError, ResultUnknown} {
		m.prepareClaims.WithLabelValues(result.String())
		m.unprepareClaims.WithLabelValues(result.String())
	}
	return m
}

func newGauge(spec metricSpec) prometheus.Gauge {
	return prometheus.NewGauge(prometheus.GaugeOpts{
		Name: spec.name,
		Help: spec.help,
	})
}

func newCounterVec(spec metricSpec) *prometheus.CounterVec {
	return prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: spec.name,
		Help: spec.help,
	}, spec.labels)
}

func newHistogram(spec metricSpec) prometheus.Histogram {
	return prometheus.NewHistogram(prometheus.HistogramOpts{
		Name:    spec.name,
		Help:    spec.help,
		Buckets: spec.buckets,
	})
}

func (m *Metrics) SetAllocationState(state AllocationState) {
	m.allocatedCPUs.Set(float64(state.AllocatedCPUs))
	m.availableCPUs.Set(float64(state.AvailableCPUs))
	m.reservedCPUs.Set(float64(state.ReservedCPUs))
	m.activeResourceClaims.Set(float64(state.ActiveResourceClaims))
}

func (m *Metrics) RecordPrepare(result Result, duration time.Duration) {
	m.prepareClaims.WithLabelValues(result.String()).Inc()
	m.prepareClaimDuration.Observe(duration.Seconds())
}

func (m *Metrics) RecordUnprepare(result Result) {
	m.unprepareClaims.WithLabelValues(result.String()).Inc()
}

func (m *Metrics) RecordClaimAllocatedCPUs(cpus int) {
	m.claimAllocatedCPUs.Observe(float64(cpus))
}

type noopRecorder struct{}

// Noop returns a recorder that discards all metric observations.
func Noop() Recorder {
	return noopRecorder{}
}

func (noopRecorder) SetAllocationState(AllocationState)  {}
func (noopRecorder) RecordPrepare(Result, time.Duration) {}
func (noopRecorder) RecordUnprepare(Result)              {}
func (noopRecorder) RecordClaimAllocatedCPUs(int)        {}
