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

package app

import (
	"context"
	"errors"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/driverconfig"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/driver"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/sysfs"
	"k8s.io/client-go/kubernetes"
)

const testWaitTimeout = 5 * time.Second

func TestRunRejectsReservedCPUsBeforeStartup(t *testing.T) {
	env := newAppTestEnv()
	cfg := driverconfig.Default()
	cfg.ReservedCPUs = "invalid"

	err := Run(context.Background(), testr.New(t), cfg, env.deps)
	if err == nil {
		t.Fatal("Run() succeeded, want reserved CPU parse error")
	}
	if calls := env.calls.entries(); len(calls) != 0 {
		t.Fatalf("dependencies called before config validation: %v", calls)
	}
}

func TestRunStopsAfterSysFSFailure(t *testing.T) {
	env := newAppTestEnv()
	wantErr := errors.New("sysfs failed")
	env.deps.NewSysFS = func(logr.Logger, string) (sysfs.FS, error) {
		return nil, wantErr
	}

	err := Run(context.Background(), testr.New(t), driverconfig.Default(), env.deps)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
	if calls := env.calls.entries(); len(calls) != 0 {
		t.Fatalf("dependencies called after sysfs failure: %v", calls)
	}
}

func TestRunUsesConfigSysFSOverlay(t *testing.T) {
	env := newAppTestEnv()
	cfg := driverconfig.Default()
	cfg.SysFSOverlay = "/test/sysfs-overlay.yaml"
	wantErr := errors.New("stop after checking sysfs path")

	var gotOverlay string
	env.deps.NewSysFS = func(_ logr.Logger, overlay string) (sysfs.FS, error) {
		gotOverlay = overlay
		return nil, wantErr
	}

	err := Run(context.Background(), testr.New(t), cfg, env.deps)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
	if gotOverlay != cfg.SysFSOverlay {
		t.Fatalf("NewSysFS() overlay = %q, want %q", gotOverlay, cfg.SysFSOverlay)
	}
}

func TestRunFailsWhenBindFails(t *testing.T) {
	env := newAppTestEnv()
	wantErr := errors.New("bind failed")
	env.deps.Listen = func(string, string) (net.Listener, error) {
		return nil, wantErr
	}

	err := Run(context.Background(), testr.New(t), driverconfig.Default(), env.deps)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
	if calls := env.calls.entries(); !slices.Equal(calls, []string{"sysfs"}) {
		t.Fatalf("calls = %v, want only sysfs before bind", calls)
	}
}

func TestRunCleansHTTPServerWhenKubeClientFails(t *testing.T) {
	testRunCleansHTTPServerAfterFailure(t, func(env *appTestEnv) error {
		wantErr := errors.New("kube client failed")
		env.deps.NewKubeClient = func(driverconfig.Config) (kubernetes.Interface, error) {
			return nil, wantErr
		}
		return wantErr
	})
}

func TestRunCleansHTTPServerWhenNodeNameFails(t *testing.T) {
	env := newAppTestEnv()
	wantErr := errors.New("node name failed")
	env.deps.NewNodeName = func(string) (string, error) {
		return "", wantErr
	}

	err := Run(context.Background(), testr.New(t), driverconfig.Default(), env.deps)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
	if want := "set hostnameOverride in the driver config or the NODE_NAME environment variable"; !strings.Contains(err.Error(), want) {
		t.Fatalf("Run() error = %q, want message containing %q", err, want)
	}
	if !env.listener.isClosed() {
		t.Fatal("HTTP listener was not closed")
	}
}

func TestRunCleansHTTPServerWhenDriverInitFails(t *testing.T) {
	testRunCleansHTTPServerAfterFailure(t, func(env *appTestEnv) error {
		wantErr := errors.New("driver init failed")
		env.deps.NewDriver = func(logr.Logger, driver.Providers, *driver.Config) (Driver, error) {
			return nil, wantErr
		}
		return wantErr
	})
}

func testRunCleansHTTPServerAfterFailure(t *testing.T, configure func(*appTestEnv) error) {
	t.Helper()

	env := newAppTestEnv()
	wantErr := configure(env)

	err := Run(context.Background(), testr.New(t), driverconfig.Default(), env.deps)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
	if !env.listener.isClosed() {
		t.Fatal("HTTP listener was not closed")
	}
}

func TestRunDoesNotStopDriverWhenStartFails(t *testing.T) {
	env := newAppTestEnv()
	wantErr := errors.New("start failed")
	env.driver.startErr = wantErr

	err := Run(context.Background(), testr.New(t), driverconfig.Default(), env.deps)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
	if got := env.driver.stopCalls.Load(); got != 0 {
		t.Fatalf("driver Stop() calls = %d, want 0", got)
	}
	if !env.listener.isClosed() {
		t.Fatal("HTTP listener was not closed")
	}
}

func TestRunStopsOnDriverError(t *testing.T) {
	env := newAppTestEnv()
	wantErr := errors.New("driver failed")
	env.driver.asyncErr <- wantErr

	err := Run(context.Background(), testr.New(t), driverconfig.Default(), env.deps)
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
	if got := env.driver.stopCalls.Load(); got != 1 {
		t.Fatalf("driver Stop() calls = %d, want 1", got)
	}
	if !env.listener.isClosed() {
		t.Fatal("HTTP listener was not closed")
	}
}

func TestRunStopsOnHTTPServerError(t *testing.T) {
	env := newAppTestEnv()
	wantErr := errors.New("serve failed")
	env.listener.acceptErrCh = make(chan error, 1)

	done := make(chan error, 1)
	logger := testr.New(t)
	go func() {
		done <- Run(context.Background(), logger, driverconfig.Default(), env.deps)
	}()

	select {
	case <-env.driver.started:
	case <-time.After(testWaitTimeout):
		t.Fatal("driver did not start")
	}

	env.listener.acceptErrCh <- wantErr

	var err error
	select {
	case err = <-done:
	case <-time.After(testWaitTimeout):
		t.Fatal("Run() did not return after HTTP server error")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("Run() error = %v, want %v", err, wantErr)
	}
	if got := env.driver.stopCalls.Load(); got != 1 {
		t.Fatalf("driver Stop() calls = %d, want 1", got)
	}
	if !env.listener.isClosed() {
		t.Fatal("HTTP listener was not closed")
	}
}

func TestRunCancellationShutsDownCleanly(t *testing.T) {
	env := newAppTestEnv()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	logger := testr.New(t)

	go func() {
		done <- Run(ctx, logger, driverconfig.Default(), env.deps)
	}()

	select {
	case <-env.driver.started:
	case <-time.After(testWaitTimeout):
		t.Fatal("driver did not start")
	}
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() error = %v, want nil after cancellation", err)
		}
	case <-time.After(testWaitTimeout):
		t.Fatal("Run() did not return after cancellation")
	}
	if got := env.driver.stopCalls.Load(); got != 1 {
		t.Fatalf("driver Stop() calls = %d, want 1", got)
	}
	if !env.listener.isClosed() {
		t.Fatal("HTTP listener was not closed")
	}
}

func TestRunJoinsShutdownError(t *testing.T) {
	env := newAppTestEnv()
	wantErr := errors.New("shutdown failed")
	env.listener.closeErr = wantErr

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	logger := testr.New(t)

	go func() {
		done <- Run(ctx, logger, driverconfig.Default(), env.deps)
	}()

	select {
	case <-env.driver.started:
	case <-time.After(testWaitTimeout):
		t.Fatal("driver did not start")
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, wantErr) {
			t.Fatalf("Run() error = %v, want %v", err, wantErr)
		}
	case <-time.After(testWaitTimeout):
		t.Fatal("Run() did not return after cancellation")
	}
}

func TestRunStartupOrder(t *testing.T) {
	env := newAppTestEnv()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	logger := testr.New(t)

	go func() {
		done <- Run(ctx, logger, driverconfig.Default(), env.deps)
	}()

	select {
	case <-env.driver.started:
	case <-time.After(testWaitTimeout):
		t.Fatal("driver did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(testWaitTimeout):
		t.Fatal("Run() did not return after cancellation")
	}

	wantCalls := []string{"sysfs", "listen", "kube", "node", "driver.new"}
	calls := env.calls.entries()
	if len(calls) < len(wantCalls) || !slices.Equal(calls[:len(wantCalls)], wantCalls) {
		t.Fatalf("startup calls = %v, want prefix %v", calls, wantCalls)
	}
}

func TestRunShutdownOrder(t *testing.T) {
	env := newAppTestEnv()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	logger := testr.New(t)

	go func() {
		done <- Run(ctx, logger, driverconfig.Default(), env.deps)
	}()

	select {
	case <-env.driver.started:
	case <-time.After(testWaitTimeout):
		t.Fatal("driver did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(testWaitTimeout):
		t.Fatal("Run() did not return after cancellation")
	}

	calls := env.calls.entries()
	closeIndex := slices.Index(calls, "http.close")
	stopIndex := slices.Index(calls, "driver.stop")
	if closeIndex == -1 || stopIndex == -1 {
		t.Fatalf("shutdown calls = %v, want http.close and driver.stop", calls)
	}
	if closeIndex > stopIndex {
		t.Fatalf("shutdown calls = %v, want HTTP server closed before driver.Stop", calls)
	}
}
