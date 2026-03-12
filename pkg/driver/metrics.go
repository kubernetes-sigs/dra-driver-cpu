/*
Copyright 2025 The Kubernetes Authors.

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

package driver

import (
	"github.com/prometheus/client_golang/prometheus"
)

const metricsSubsystem = "dra_cpu"

var (
	prepareClaimSuccessTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Subsystem: metricsSubsystem,
		Name:      "prepare_claims_success_total",
		Help:      "Total number of successful PrepareResourceClaims operations.",
	})

	prepareClaimErrorTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Subsystem: metricsSubsystem,
		Name:      "prepare_claims_error_total",
		Help:      "Total number of failed PrepareResourceClaims operations.",
	})

	unprepareClaimTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Subsystem: metricsSubsystem,
		Name:      "unprepare_claims_total",
		Help:      "Total number of UnprepareResourceClaims operations.",
	})

	prepareClaimDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
		Subsystem: metricsSubsystem,
		Name:      "prepare_claim_duration_seconds",
		Help:      "Duration of individual resource claim prepare operations in seconds.",
		Buckets:   prometheus.DefBuckets,
	})

	claimAllocatedCPUs = prometheus.NewHistogram(prometheus.HistogramOpts{
		Subsystem: metricsSubsystem,
		Name:      "claim_allocated_cpus",
		Help:      "Number of CPUs allocated per claim.",
		Buckets:   []float64{1, 2, 4, 8, 16, 32, 64, 128},
	})
)

func init() {
	prometheus.MustRegister(
		prepareClaimSuccessTotal,
		prepareClaimErrorTotal,
		unprepareClaimTotal,
		prepareClaimDuration,
		claimAllocatedCPUs,
	)
}
