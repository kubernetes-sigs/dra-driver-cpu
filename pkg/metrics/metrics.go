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

package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

const Subsystem = "dra_cpu"

// Metrics bundles all Prometheus collectors used by the driver.
// Struct-based design enables dependency injection and hermetic tests.
type Metrics struct {
	PrepareClaimTotal    *prometheus.CounterVec
	UnprepareClaimTotal  prometheus.Counter
	PrepareClaimDuration prometheus.Histogram
	ClaimAllocatedCPUs   prometheus.Histogram

	AllocatedCPUs        prometheus.Gauge
	AvailableCPUs        prometheus.Gauge
	ReservedCPUs         prometheus.Gauge
	ActiveResourceClaims prometheus.Gauge
}

// New creates a Metrics instance and registers all collectors with the
// provided registerer. Passing a fresh registry makes tests independent.
func New(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		PrepareClaimTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Subsystem: Subsystem,
			Name:      "prepare_claim_total",
			Help:      "Total PrepareResourceClaims operations, by result.",
		}, []string{"result"}),
		UnprepareClaimTotal: prometheus.NewCounter(prometheus.CounterOpts{
			Subsystem: Subsystem,
			Name:      "unprepare_claims_total",
			Help:      "Total UnprepareResourceClaims operations.",
		}),
		PrepareClaimDuration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Subsystem: Subsystem,
			Name:      "prepare_claim_duration_seconds",
			Help:      "Duration of prepare operations in seconds.",
			Buckets:   prometheus.DefBuckets,
		}),
		ClaimAllocatedCPUs: prometheus.NewHistogram(prometheus.HistogramOpts{
			Subsystem: Subsystem,
			Name:      "claim_allocated_cpus",
			Help:      "Number of CPUs allocated per claim.",
			Buckets:   []float64{1, 2, 4, 8, 16, 32, 64, 128, 256, 512, 1024},
		}),
		AllocatedCPUs: prometheus.NewGauge(prometheus.GaugeOpts{
			Subsystem: Subsystem, Name: "allocated_cpus",
			Help: "Number of CPUs currently allocated to resource claims.",
		}),
		AvailableCPUs: prometheus.NewGauge(prometheus.GaugeOpts{
			Subsystem: Subsystem, Name: "available_cpus",
			Help: "Number of CPUs available for allocation.",
		}),
		ReservedCPUs: prometheus.NewGauge(prometheus.GaugeOpts{
			Subsystem: Subsystem, Name: "reserved_cpus",
			Help: "Number of CPUs reserved and excluded from allocation.",
		}),
		ActiveResourceClaims: prometheus.NewGauge(prometheus.GaugeOpts{
			Subsystem: Subsystem, Name: "resource_claims_active",
			Help: "Number of currently active resource claim allocations.",
		}),
	}
	reg.MustRegister(
		m.PrepareClaimTotal,
		m.UnprepareClaimTotal,
		m.PrepareClaimDuration,
		m.ClaimAllocatedCPUs,
		m.AllocatedCPUs,
		m.AvailableCPUs,
		m.ReservedCPUs,
		m.ActiveResourceClaims,
	)
	return m
}

// RecordPrepareResult is a helper: label "success" or "error".
func (m *Metrics) RecordPrepareResult(err error) {
	label := "success"
	if err != nil {
		label = "error"
	}
	m.PrepareClaimTotal.WithLabelValues(label).Inc()
}

// Default is the process-wide Metrics instance registered with the default
// Prometheus registry. Tests may replace via ResetForTest.
var Default = New(prometheus.DefaultRegisterer)

// ResetForTest replaces Default with a fresh Metrics bound to a new registry.
// Returns the new registry for assertions. Test-only.
func ResetForTest() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	Default = New(reg)
	return reg
}
