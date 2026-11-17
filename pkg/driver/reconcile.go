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

	"github.com/containerd/nri/pkg/api"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/ctxlog"
	"k8s.io/apimachinery/pkg/types"
)

// containerUpdater is the one stub method the worker needs, so tests can fake it.
type containerUpdater interface {
	UpdateContainers([]*api.ContainerUpdate) ([]*api.ContainerUpdate, error)
}

// runReconcileWorker sends the updates from its own goroutine. Unprepare must
// not wait on an NRI round trip, and an unsolicited update sent from inside an
// NRI hook is the deadlock containerd/nri#301 fixed on the runtime side.
func (cp *CPUDriver) runReconcileWorker(ctx context.Context) {
	logger := ctxlog.FromContext(ctx)
	logger.V(2).Info("reconcile worker started")
	defer logger.V(2).Info("reconcile worker stopped")

	for {
		select {
		case <-ctx.Done():
			return
		case <-cp.reconcileTrigger:
			cp.reconcileSharedContainers(ctx)
		}
	}
}

// requestReconcile never blocks: Unprepare calls it with stateMu held. The
// trigger holds one token, so a burst of releases is one pass.
func (cp *CPUDriver) requestReconcile() {
	if cp.reconcileTrigger == nil {
		return
	}
	select {
	case cp.reconcileTrigger <- struct{}{}:
	default:
	}
}

// reconcileSharedContainers puts every running shared container on the current
// shared pool.
//
// The cpuset is a snapshot: stateMu cannot be held across the runtime call, so
// by the time the runtime applies it a Prepare may have taken some of those
// CPUs back, and the stale update would hand them to shared containers again.
// pendingSharedUpdate marks that window and Prepare waits for it to close.
//
// A failed update is not retried. The containers keep the narrower cpuset and
// get the wider one at their next lifecycle event, as they do without this option.
func (cp *CPUDriver) reconcileSharedContainers(ctx context.Context) {
	logger := ctxlog.FromContext(ctx)
	if cp.containerUpdater == nil {
		return
	}

	cp.stateMu.Lock()
	updates, err := sharedContainerUpdatesFor(logger, cp.allocationState, cp.podState, types.UID(""))
	if err != nil || len(updates) == 0 {
		cp.stateMu.Unlock()
		if err != nil {
			logger.Error(err, "cannot reconcile shared containers")
		}
		return
	}
	pending := make(chan struct{})
	cp.pendingSharedUpdate = pending
	cp.stateMu.Unlock()

	logger.V(2).Info("reconciling shared containers after claim release", "numContainers", len(updates))
	failed, err := cp.containerUpdater.UpdateContainers(updates)

	cp.stateMu.Lock()
	cp.pendingSharedUpdate = nil
	close(pending)
	turn := cp.prepareTurn
	cp.stateMu.Unlock()
	// Let the Prepares woken by close(pending) take the lock before the next
	// round. Without this the worker wins the race every time and a Prepare sits
	// through the whole burst.
	if turn != nil {
		<-turn
	}

	if err != nil {
		logger.Error(err, "shared container reconcile did not complete", "numFailed", len(failed))
		return
	}
	for _, update := range failed {
		logger.Info("runtime refused a shared container update", "containerID", update.GetContainerId())
	}
}

// lockStateForPrepare takes stateMu once no shared-container update is in
// flight. The caller unlocks. The wait happens without the lock: the worker
// needs it to clear the marker and Synchronize needs it to close initialSync.
// Waiters register in prepareTurn so the worker holds its next round until
// they have the lock.
func (cp *CPUDriver) lockStateForPrepare(ctx context.Context) error {
	cp.stateMu.Lock()
	for cp.pendingSharedUpdate != nil {
		pending := cp.pendingSharedUpdate
		cp.prepareWaiters++
		if cp.prepareTurn == nil {
			cp.prepareTurn = make(chan struct{})
		}
		cp.stateMu.Unlock()
		select {
		case <-pending:
		case <-ctx.Done():
		}
		cp.stateMu.Lock()
		cp.prepareWaiters--
		if cp.prepareWaiters == 0 {
			close(cp.prepareTurn)
			cp.prepareTurn = nil
		}
		if err := ctx.Err(); err != nil {
			cp.stateMu.Unlock()
			return fmt.Errorf("shared container update still in flight: %w", err)
		}
	}
	return nil
}
