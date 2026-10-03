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
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/stub"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/ctxlog"
)

const (
	nriStartupTimeout = 30 * time.Second
	nriStopTimeout    = 5 * time.Second
)

// A stuck stub must not be retried: older NRI versions can block in Start
// waiting for Configure while also preventing Stop from acquiring their lock.
var errNRIUnresponsive = errors.New("NRI plugin did not respond; process restart required")

type nriPlugin interface {
	Run(context.Context) error
	Stop()
}

type nriAttempt struct {
	ready  bool
	closed bool
}

// mu only protects connection state. When both locks are needed the order is
// CPUDriver.stateMu -> mu. No RPC, I/O, or goroutine join may hold mu.
type nriLifecycle struct {
	mu             sync.Mutex
	current        *nriAttempt
	changed        chan struct{}
	cancel         context.CancelFunc
	done           chan struct{}
	err            error // written before done closes
	startupTimeout time.Duration
	stopTimeout    time.Duration
}

type nriAttemptHandler struct {
	*CPUDriver
	attempt *nriAttempt
}

func (h *nriAttemptHandler) Synchronize(ctx context.Context, pods []*api.PodSandbox, containers []*api.Container) ([]*api.ContainerUpdate, error) {
	return h.synchronize(ctx, pods, containers, h.attempt)
}

func (h *nriAttemptHandler) CreateContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
	return h.createContainer(ctx, pod, ctr, h.attempt)
}

func (h *nriAttemptHandler) StopContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) ([]*api.ContainerUpdate, error) {
	return h.stopContainer(ctx, pod, ctr, h.attempt)
}

func (h *nriAttemptHandler) RemoveContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) error {
	return h.removeContainer(ctx, pod, ctr, h.attempt)
}

// Called with stateMu held, before a lifecycle callback mutates live state.
func (cp *CPUDriver) checkNRIRequest(ctx context.Context, attempt *nriAttempt) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if attempt == nil {
		return nil
	}
	cp.nri.mu.Lock()
	defer cp.nri.mu.Unlock()
	if cp.nri.current != attempt || attempt.closed || !attempt.ready {
		return fmt.Errorf("NRI connection is not synchronized")
	}
	return nil
}

func (h *nriAttemptHandler) Shutdown(ctx context.Context) {
	h.nri.closeAttempt(h.attempt)
	h.CPUDriver.Shutdown(ctx)
}

func (cp *CPUDriver) newNRIStub(attempt *nriAttempt) (nriPlugin, error) {
	return stub.New(&nriAttemptHandler{CPUDriver: cp, attempt: attempt},
		stub.WithPluginName(cp.driverName),
		stub.WithPluginIdx("00"),
		// Supplying OnClose also prevents the stub from exiting the process.
		stub.WithOnClose(func() { cp.nri.closeAttempt(attempt) }),
	)
}

// startPlugins is the startup ordering boundary. DRA is not even registered
// until this connection has committed a complete local synchronization.
// Synchronize success is not an acknowledgement of runtime update application.
func (cp *CPUDriver) startPlugins(ctx context.Context, newNRI func(*nriAttempt) (nriPlugin, error), startDRA func(context.Context) (KubeletPlugin, error)) error {
	runCtx, cancel := context.WithCancel(ctx)
	cp.nri = &nriLifecycle{
		changed: make(chan struct{}), cancel: cancel, done: make(chan struct{}),
		startupTimeout: nriStartupTimeout, stopTimeout: nriStopTimeout,
	}
	go func() {
		defer close(cp.nri.done)
		cp.nri.err = runNRIPluginWithRetry(runCtx, &nriConnectionRunner{cp: cp, newPlugin: newNRI}, maxAttempts)
	}()
	waitCtx, stopWaiting := context.WithTimeout(ctx, nriStartupTimeout)
	defer stopWaiting()
	if err := cp.nri.waitReady(waitCtx); err != nil {
		cancel()
		return fmt.Errorf("wait for NRI synchronization: %w", err)
	}
	d, err := startDRA(runCtx)
	if err != nil {
		cancel()
		return fmt.Errorf("start kubelet plugin: %w", err)
	}
	cp.draPlugin = d
	if err := waitForRegistration(runCtx, d, registrarDir(cp.kubeletRootDir), registrationPollInterval, registrationTimeout); err != nil {
		cancel()
		return err
	}
	return nil
}

func (n *nriLifecycle) notifyLocked() {
	close(n.changed)
	n.changed = make(chan struct{})
}

func (n *nriLifecycle) closeAttempt(a *nriAttempt) {
	n.mu.Lock()
	defer n.mu.Unlock()
	a.closed = true
	a.ready = false
	n.notifyLocked()
}

func (n *nriLifecycle) waitReady(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n.mu.Lock()
		ready := n.current != nil && n.current.ready && !n.current.closed
		changed := n.changed
		n.mu.Unlock()
		if ready {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-n.done:
			return n.err
		case <-changed:
		}
	}
}

// Called with stateMu held. Cleanup remains permitted while disconnected;
// only new prepares require the current NRI connection to be synchronized.
func (cp *CPUDriver) prepareReady(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if cp.nri == nil {
		return nil // Direct hook users without a running service (including unit tests).
	}
	cp.nri.mu.Lock()
	defer cp.nri.mu.Unlock()
	if a := cp.nri.current; a == nil || !a.ready || a.closed {
		return fmt.Errorf("NRI state is not synchronized; retry preparation")
	}
	return nil
}

type nriConnectionRunner struct {
	cp        *CPUDriver
	newPlugin func(*nriAttempt) (nriPlugin, error)
}

func (r *nriConnectionRunner) Run(ctx context.Context) error {
	n := r.cp.nri
	a := &nriAttempt{}
	plugin, err := r.newPlugin(a)
	if err != nil {
		return err
	}
	n.mu.Lock()
	n.current = a
	n.notifyLocked()
	n.mu.Unlock()

	attemptCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	result := make(chan error, 1)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		result <- plugin.Run(attemptCtx)
	}()
	timer := time.NewTimer(n.startupTimeout)
	defer timer.Stop()
	timeout := timer.C
	var runErr error
	for {
		n.mu.Lock()
		changed := n.changed
		ready := a.ready && !a.closed
		closed := a.closed
		n.mu.Unlock()
		if closed {
			runErr = fmt.Errorf("NRI connection closed")
			break
		}
		if ready {
			timer.Stop()
			timeout = nil
		}
		select {
		case <-ctx.Done():
			runErr = ctx.Err()
		case runErr = <-result:
		case <-timeout:
			runErr = fmt.Errorf("%w: synchronization timed out", errNRIUnresponsive)
		case <-changed:
			continue
		}
		break
	}
	n.closeAttempt(a)
	cancel()
	// Stop is deliberately bounded outside both locks. NRI v0.11 can remain
	// stuck before Configure; abandon that attempt and terminate, never retry it.
	stopped := make(chan struct{})
	go func() {
		plugin.Stop()
		<-finished
		close(stopped)
	}()
	stopTimer := time.NewTimer(n.stopTimeout)
	defer stopTimer.Stop()
	select {
	case <-stopped:
	case <-stopTimer.C:
		err := fmt.Errorf("%w: Stop timed out", errNRIUnresponsive)
		ctxlog.FromContext(ctx).Error(err, "unable to stop NRI plugin")
		return err
	}
	return runErr
}
