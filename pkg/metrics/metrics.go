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

const (
	subsystem = "dra_cpu"

	ResultSuccess = "success"
	ResultError   = "error"
)

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
	RecordPrepare(result string, duration time.Duration)
	RecordUnprepare(result string)
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

var descriptors = []Descriptor{
	{
		Name:      "dra_cpu_allocated_cpus",
		Type:      "gauge",
		Stability: "ALPHA",
		Help:      "Number of CPUs currently allocated to prepared resource claims.",
	},
	{
		Name:      "dra_cpu_available_cpus",
		Type:      "gauge",
		Stability: "ALPHA",
		Help:      "Number of CPUs available for allocation after reserved and active claim CPUs are excluded.",
	},
	{
		Name:      "dra_cpu_reserved_cpus",
		Type:      "gauge",
		Stability: "ALPHA",
		Help:      "Number of CPUs reserved and excluded from DRA management.",
	},
	{
		Name:      "dra_cpu_resource_claims_active",
		Type:      "gauge",
		Stability: "ALPHA",
		Help:      "Number of resource claims currently recorded as active by the allocation store.",
	},
	{
		Name:      "dra_cpu_prepare_claims_total",
		Type:      "counter",
		Stability: "ALPHA",
		Help:      "Total number of per-claim PrepareResourceClaims results.",
		Labels:    []string{"result"},
	},
	{
		Name:      "dra_cpu_unprepare_claims_total",
		Type:      "counter",
		Stability: "ALPHA",
		Help:      "Total number of per-claim UnprepareResourceClaims results.",
		Labels:    []string{"result"},
	},
	{
		Name:      "dra_cpu_prepare_claim_duration_seconds",
		Type:      "histogram",
		Stability: "ALPHA",
		Help:      "Duration of per-claim prepare operations in seconds.",
	},
	{
		Name:      "dra_cpu_claim_allocated_cpus",
		Type:      "histogram",
		Stability: "ALPHA",
		Help:      "Number of CPUs allocated for each newly successful claim allocation.",
	},
}

// Descriptors returns metadata for custom CPU driver metrics.
func Descriptors() []Descriptor {
	out := make([]Descriptor, len(descriptors))
	for i, descriptor := range descriptors {
		out[i] = descriptor
		out[i].Labels = append([]string{}, descriptor.Labels...)
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
		allocatedCPUs: prometheus.NewGauge(prometheus.GaugeOpts{
			Subsystem: subsystem,
			Name:      "allocated_cpus",
			Help:      descriptors[0].Help,
		}),
		availableCPUs: prometheus.NewGauge(prometheus.GaugeOpts{
			Subsystem: subsystem,
			Name:      "available_cpus",
			Help:      descriptors[1].Help,
		}),
		reservedCPUs: prometheus.NewGauge(prometheus.GaugeOpts{
			Subsystem: subsystem,
			Name:      "reserved_cpus",
			Help:      descriptors[2].Help,
		}),
		activeResourceClaims: prometheus.NewGauge(prometheus.GaugeOpts{
			Subsystem: subsystem,
			Name:      "resource_claims_active",
			Help:      descriptors[3].Help,
		}),
		prepareClaims: prometheus.NewCounterVec(prometheus.CounterOpts{
			Subsystem: subsystem,
			Name:      "prepare_claims_total",
			Help:      descriptors[4].Help,
		}, []string{"result"}),
		unprepareClaims: prometheus.NewCounterVec(prometheus.CounterOpts{
			Subsystem: subsystem,
			Name:      "unprepare_claims_total",
			Help:      descriptors[5].Help,
		}, []string{"result"}),
		prepareClaimDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Subsystem: subsystem,
			Name:      "prepare_claim_duration_seconds",
			Help:      descriptors[6].Help,
			Buckets:   prometheus.DefBuckets,
		}),
		claimAllocatedCPUs: prometheus.NewHistogram(prometheus.HistogramOpts{
			Subsystem: subsystem,
			Name:      "claim_allocated_cpus",
			Help:      descriptors[7].Help,
			Buckets:   []float64{1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024},
		}),
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
	return m
}

func (m *Metrics) SetAllocationState(state AllocationState) {
	m.allocatedCPUs.Set(float64(state.AllocatedCPUs))
	m.availableCPUs.Set(float64(state.AvailableCPUs))
	m.reservedCPUs.Set(float64(state.ReservedCPUs))
	m.activeResourceClaims.Set(float64(state.ActiveResourceClaims))
}

func (m *Metrics) RecordPrepare(result string, duration time.Duration) {
	m.prepareClaims.WithLabelValues(result).Inc()
	m.prepareClaimDuration.Observe(duration.Seconds())
}

func (m *Metrics) RecordUnprepare(result string) {
	m.unprepareClaims.WithLabelValues(result).Inc()
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
func (noopRecorder) RecordPrepare(string, time.Duration) {}
func (noopRecorder) RecordUnprepare(string)              {}
func (noopRecorder) RecordClaimAllocatedCPUs(int)        {}
