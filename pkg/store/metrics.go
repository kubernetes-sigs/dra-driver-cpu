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

package store

import (
	"github.com/prometheus/client_golang/prometheus"
)

const metricsSubsystem = "dra_cpu"

var (
	allocatedCPUsGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Subsystem: metricsSubsystem,
		Name:      "allocated_cpus",
		Help:      "Number of CPUs currently allocated to resource claims.",
	})

	availableCPUsGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Subsystem: metricsSubsystem,
		Name:      "available_cpus",
		Help:      "Number of CPUs available for allocation.",
	})

	reservedCPUsGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Subsystem: metricsSubsystem,
		Name:      "reserved_cpus",
		Help:      "Number of CPUs reserved and excluded from allocation.",
	})

	activeResourceClaimsGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Subsystem: metricsSubsystem,
		Name:      "resource_claims_active",
		Help:      "Number of currently active resource claim allocations.",
	})
)

func init() {
	prometheus.MustRegister(
		allocatedCPUsGauge,
		availableCPUsGauge,
		reservedCPUsGauge,
		activeResourceClaimsGauge,
	)
}
