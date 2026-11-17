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
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/containerd/nri/pkg/api"
	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
	cpumetrics "github.com/kubernetes-sigs/dra-driver-cpu/pkg/metrics"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/utils/cpuset"
)

// slowRuntime makes every UpdateContainers take rpc and counts completed rounds.
type slowRuntime struct {
	rpc    time.Duration
	rounds atomic.Int64
}

func (r *slowRuntime) UpdateContainers([]*api.ContainerUpdate) ([]*api.ContainerUpdate, error) {
	time.Sleep(r.rpc)
	r.rounds.Add(1)
	return nil, nil
}

// A Prepare that arrives while Unprepares keep the worker busy must wait for the
// round in flight, not for the whole burst. Without prepareTurn the worker won
// the race for stateMu after every round and a Prepare sat through 3-14 rounds
// here.
func TestReconcilePrepareUnderUnprepareBurst(t *testing.T) {
	const numCPUs = 64
	const burst = 48
	const rpc = 10 * time.Millisecond

	logger := testr.New(t)
	var infos []cpuinfo.CPUInfo
	names := map[string]int{}
	for cpu := range numCPUs {
		infos = append(infos, cpuinfo.CPUInfo{CpuID: cpu, CoreID: cpu, SocketID: 0, NUMANodeID: 0, SiblingCPUID: -1})
		names[fmt.Sprintf("cpudev%d", cpu)] = cpu
	}
	topo, err := (&cpuinfo.MockCPUInfoProvider{CPUInfos: infos}).GetCPUTopology(logger)
	require.NoError(t, err)

	runtime := &slowRuntime{rpc: rpc}
	d := &CPUDriver{
		driverName: testDriverName,
		topology: deviceTopology{
			Inventory: device.Inventory{CPUTopology: topo},
			Mapping:   device.Mapping{NameToCPUID: names},
		},
		podState:         store.NewPodState(),
		allocationState:  store.NewAllocationState(topo, cpuset.New()),
		cdiMgr:           newMockCdiMgr(),
		metrics:          cpumetrics.Noop(),
		containerUpdater: runtime,
		reconcileTrigger: make(chan struct{}, 1),
	}
	_, err = d.Synchronize(t.Context(), nil, nil)
	require.NoError(t, err)

	// One shared container so the worker has something to update.
	sharedPod := &api.PodSandbox{Id: "shared-sandbox", Uid: "shared-pod"}
	_, _, err = d.CreateContainer(t.Context(), sharedPod, &api.Container{Id: "shared-id", Name: "shared", PodSandboxId: sharedPod.Id})
	require.NoError(t, err)

	// Prepare the claims to be released before the worker starts.
	released := make([]*resourceapi.ResourceClaim, 0, burst)
	for i := range burst {
		claim := individualMetricsClaim(types.UID(fmt.Sprintf("claim-%d", i)), fmt.Sprintf("cpudev%d", numCPUs-1-i))
		results, err := d.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
		require.NoError(t, err)
		require.NoError(t, results[claim.UID].Err)
		released = append(released, claim)
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	workerDone := make(chan struct{})
	go func() {
		d.runReconcileWorker(ctx)
		close(workerDone)
	}()

	// Keep the trigger topped up: one Unprepare per quarter RPC.
	burstDone := make(chan struct{})
	go func() {
		defer close(burstDone)
		for _, claim := range released {
			results, err := d.UnprepareResourceClaims(ctx, []kubeletplugin.NamespacedObject{{UID: claim.UID}})
			if err != nil || results[claim.UID] != nil {
				return
			}
			time.Sleep(rpc / 4)
		}
	}()

	// Let the burst get going, then time one Prepare.
	time.Sleep(2*rpc + rpc/2)
	before := runtime.rounds.Load()
	start := time.Now()
	newClaim := individualMetricsClaim("claim-new", "cpudev0")
	results, err := d.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{newClaim})
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.NoError(t, results[newClaim.UID].Err)
	roundsWaited := runtime.rounds.Load() - before

	<-burstDone
	cancel()
	<-workerDone

	t.Logf("Prepare waited %v across %d worker rounds (rpc=%v, %d Unprepares in flight)", elapsed, roundsWaited, rpc, burst)
	require.LessOrEqual(t, roundsWaited, int64(2), "Prepare should wait for at most the in-flight round plus one")
}
