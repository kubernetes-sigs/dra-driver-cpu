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

package driver

import (
	"time"

	cpumetrics "github.com/kubernetes-sigs/dra-driver-cpu/pkg/metrics"
)

type Recorder interface {
	SetAllocationState(cpumetrics.AllocationState)
	RecordPrepare(result cpumetrics.Result, duration time.Duration)
	RecordUnprepare(result cpumetrics.Result, duration time.Duration)
	RecordClaimAllocatedCPUs(cpus int)
	RecordNRISynchronize(err error, elapsed time.Duration)
	RecordNRICreateContainer(err error, claimCount int, elapsed time.Duration)
	RecordNRIStopContainer(err error, claimCount int, elapsed time.Duration)
	RecordNRIRemoveContainer(err error, claimCount int, elapsed time.Duration)
}

func (cp *CPUDriver) refreshAllocationMetrics() {
	if cp.cpuAllocationStore == nil {
		return
	}
	snapshot := cp.cpuAllocationStore.Snapshot()
	cp.metrics.SetAllocationState(cpumetrics.AllocationState{
		AllocatedCPUs:        snapshot.AllocatedCPUs,
		AvailableCPUs:        snapshot.AvailableCPUs,
		ReservedCPUs:         snapshot.ReservedCPUs,
		ActiveResourceClaims: snapshot.ActiveResourceClaims,
	})
}
