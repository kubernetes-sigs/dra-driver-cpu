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

package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/go-logr/logr"
	dracpuapi "github.com/kubernetes-sigs/dra-driver-cpu/api"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/buildinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/ctxlog"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/driverconfig"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/driver"
	cpumetrics "github.com/kubernetes-sigs/dra-driver-cpu/pkg/metrics"
	"k8s.io/utils/cpuset"
)

// Run starts the driver and blocks until the context is canceled or a runtime
// component fails.
func Run(ctx context.Context, logger logr.Logger, cfg driverconfig.Config, deps Dependencies) (runErr error) {
	ctx = ctxlog.NewContext(ctx, logger)
	ctx, cancel := context.WithCancelCause(ctx)

	var httpServer *httpServer
	var dracpu Driver
	driverStarted := false
	defer func() {
		cancel(nil)
		if httpServer != nil {
			if err := httpServer.shutdown(httpShutdownTimeout); err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("HTTP server shutdown error: %w", err))
			}
		}
		if driverStarted {
			dracpu.Stop()
		}
	}()

	printVersion(logger)
	logger.Info("configuration successfully loaded", "configuration", cfg.Dump())

	if stop, err := startupStopCause(ctx); stop {
		return err
	}

	reservedCPUSet, err := cpuset.Parse(cfg.ReservedCPUs)
	if err != nil {
		return fmt.Errorf("failed to parse reserved CPUs: %w", err)
	}

	sfs, err := deps.NewSysFS(logger, cfg.SysFSOverlay)
	if err != nil {
		return err
	}

	httpServer, err = startHTTPServer(deps.Listen, cfg.BindAddress, deps.Metrics)
	if err != nil {
		return fmt.Errorf("failed to bind HTTP server to %s: %w", cfg.BindAddress, err)
	}
	go monitorHTTPServer(ctx, cancel, httpServer.errors())

	clientset, err := deps.NewKubeClient(cfg)
	if err != nil {
		return err
	}
	if stop, err := startupStopCause(ctx); stop {
		return err
	}

	nodeName, err := deps.NewNodeName(cfg.HostnameOverride)
	if err != nil {
		return fmt.Errorf("can not obtain the node name, use the hostname-override flag if you want to set it to a specific value: %w", err)
	}
	if stop, err := startupStopCause(ctx); stop {
		return err
	}

	driverConfig := driver.Config{
		DriverName:                            dracpuapi.DriverName,
		NodeName:                              nodeName,
		ReservedCPUs:                          reservedCPUSet,
		CPUDeviceMode:                         cfg.CPUDeviceMode,
		CPUDeviceGroupBy:                      cfg.GroupBy,
		ExposePCIeRoots:                       cfg.ExposePCIeRoots,
		PublishNodeAllocatableResourceMapping: cfg.PublishNodeAllocatableResourceMapping,
		Metrics:                               cpumetrics.New(deps.Metrics.Registerer),
		KubeletRootDir:                        cfg.KubeletRootDir,
		Allocator:                             cfg.Allocator,
	}
	driverProviders := driver.Providers{
		K8SClient: clientset,
		SysFS:     sfs,
	}
	dracpu, err = deps.NewDriver(logger, driverProviders, &driverConfig)
	if err != nil {
		return fmt.Errorf("driver failed to initialize: %w", err)
	}
	if stop, err := startupStopCause(ctx); stop {
		return err
	}

	asyncErr, err := dracpu.Start(ctx)
	if err != nil {
		if stop, cause := startupStopCause(ctx); stop {
			return cause
		}
		return fmt.Errorf("driver failed to start: %w", err)
	}
	driverStarted = true

	httpServer.setReady(true)
	logger.Info("driver started")

	runErr = waitForRuntimeFailure(ctx, asyncErr)
	httpServer.setReady(false)
	cancel(nil)
	return runErr
}

func monitorHTTPServer(ctx context.Context, cancel context.CancelCauseFunc, httpErr <-chan error) {
	select {
	case err, ok := <-httpErr:
		if !ok {
			cancel(errors.New("HTTP server stopped unexpectedly"))
			return
		}
		cancel(fmt.Errorf("HTTP server failed: %w", err))
	case <-ctx.Done():
	}
}

func startupStopCause(ctx context.Context) (bool, error) {
	cause := context.Cause(ctx)
	if cause == nil {
		return false, nil
	}
	if errors.Is(cause, context.Canceled) {
		return true, nil
	}
	return true, cause
}

func waitForRuntimeFailure(ctx context.Context, driverErr <-chan error) error {
	select {
	case <-ctx.Done():
		_, err := startupStopCause(ctx)
		return err
	case err, ok := <-driverErr:
		if !ok || err == nil {
			return errors.New("NRI driver error channel closed without an error")
		}
		return fmt.Errorf("NRI driver error: %w", err)
	}
}

func printVersion(logger logr.Logger) {
	info := buildinfo.Read()
	if info == (buildinfo.Info{}) {
		return
	}
	logger.Info("dracpu", "goVersion", info.GoVersion, "build", info.VCSRevision, "time", info.VCSTime)
}
