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

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/buildinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/ctxlog"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/driverconfig"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/gatherinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/driver"
	cpumetrics "github.com/kubernetes-sigs/dra-driver-cpu/pkg/metrics"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sys/unix"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	nodeutil "k8s.io/component-helpers/node/util"
	"k8s.io/utils/cpuset"
)

const (
	driverName = "dra.cpu"
)

var (
	driverFlags = driverconfig.Default()
	configFile  string

	ready atomic.Bool
)

func init() {
	// --config is kept outside Config to avoid a self-referential loop,
	// following the convention used by kubeadm and similar tools.
	flag.StringVar(&configFile, "config", "",
		"Path to a YAML driver configuration file. Configuration values are applied in order: "+
			"built-in defaults, then file values, then explicit CLI flags. "+
			"Only values explicitly set on the command line override earlier layers. "+
			"If empty, only CLI flags and built-in defaults are used.")
	driverFlags.AddFlags(flag.CommandLine)
}

func main() {
	if filepath.Base(os.Args[0]) == "dracpu-gatherinfo" {
		logger := ctxlog.Setup()
		if err := gatherinfo.Run(os.Args[1:], gatherinfo.Options{
			DriverConfig: driverFlags,
		}, logger); err != nil {
			fmt.Fprintf(os.Stderr, "dracpu-gatherinfo: %v\n", err)
			os.Exit(1)
		}
		return
	}

	ctxlog.AddFlags(flag.CommandLine)
	flag.Parse()

	if driverFlags.ShowMetrics {
		if err := printMetricsMetadata(os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "print metrics metadata: %v\n", err)
			os.Exit(1)
		}
		return
	}

	logger := ctxlog.Setup()

	cfg, err := driverconfig.Load(driverFlags, configFile, flag.CommandLine, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dracpu: failed to load configuration: %v\n", err)
		os.Exit(1)
	}

	if err := runDriver(logger, cfg); err != nil {
		os.Exit(1)
	}
}

func runDriver(logger logr.Logger, cfg driverconfig.Config) error {
	if err := run(logger, cfg); err != nil {
		logger.Error(err, "failed to run")
		return err
	}
	return nil
}

func run(logger logr.Logger, cfg driverconfig.Config) error {
	printVersion(logger)
	logger.Info("CONFIG", cfg.LogValues()...)

	reservedCPUSet, err := cpuset.Parse(cfg.ReservedCPUs)
	if err != nil {
		return fmt.Errorf("failed to parse reserved CPUs: %w", err)
	}

	mux := http.NewServeMux()
	// Add healthz handler
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	})
	// Add metrics handler
	mux.Handle("/metrics", promhttp.Handler())
	server := &http.Server{
		Addr:              cfg.BindAddress,
		Handler:           mux,
		IdleTimeout:       120 * time.Second,
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
	}

	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error(err, "HTTP server failed")
		}
	}()

	var restConfig *rest.Config
	if cfg.Kubeconfig != "" {
		restConfig, err = clientcmd.BuildConfigFromFlags("", cfg.Kubeconfig)
	} else {
		// creates the in-cluster config
		restConfig, err = rest.InClusterConfig()
	}
	if err != nil {
		return fmt.Errorf("can not create client-go configuration: %w", err)
	}

	// use protobuf for better performance at scale
	// https://kubernetes.io/docs/reference/using-api/api-concepts/#alternate-representations-of-resources
	restConfig.AcceptContentTypes = "application/vnd.kubernetes.protobuf,application/json"
	restConfig.ContentType = "application/vnd.kubernetes.protobuf"

	// creates the clientset
	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("can not create client-go client: %w", err)
	}

	nodeName, err := nodeutil.GetHostname(cfg.HostnameOverride)
	if err != nil {
		return fmt.Errorf("can not obtain the node name, use the hostname-override flag if you want to set it to a specific value: %w", err)
	}

	// trap Ctrl+C and call cancel on the context
	ctx := ctxlog.NewContext(context.Background(), logger)
	ctx, cancel := context.WithCancel(ctx)

	// Enable signal handler
	signalCh := make(chan os.Signal, 2)
	defer func() {
		close(signalCh)
		cancel()
	}()
	signal.Notify(signalCh, os.Interrupt, unix.SIGINT)

	driverConfig := driver.Config{
		DriverName:       driverName,
		NodeName:         nodeName,
		ReservedCPUs:     reservedCPUSet,
		CPUDeviceMode:    cfg.CPUDeviceMode,
		CPUDeviceGroupBy: cfg.GroupBy,
		ExposePCIeRoots:  cfg.ExposePCIeRoots,
		Metrics:          cpumetrics.New(prometheus.DefaultRegisterer),
	}
	driverProviders := driver.Providers{
		K8SClient: clientset,
	}
	dracpu, err := driver.New(logger, driverProviders, &driverConfig)
	if err != nil {
		return fmt.Errorf("driver failed to initialize: %w", err)
	}
	asyncErr, err := dracpu.Start(ctx)
	if err != nil {
		return fmt.Errorf("driver failed to start: %w", err)
	}
	defer dracpu.Stop()
	ready.Store(true)
	logger.Info("driver started")

	var fatalErr error

	select {
	case <-signalCh:
		logger.Info("exiting", "reason", "received signal")
		cancel()
	case <-ctx.Done():
		logger.Info("exiting", "reason", "context cancelled")
	case err := <-asyncErr:
		cancel()
		fatalErr = fmt.Errorf("NRI driver error: %w", err)
	}

	// Gracefully shutdown HTTP server
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if serverErr := server.Shutdown(shutdownCtx); serverErr != nil {
		fatalErr = errors.Join(fatalErr, fmt.Errorf("HTTP server shutdown error: %w", serverErr))
	}
	return fatalErr
}

func printVersion(logger logr.Logger) {
	info := buildinfo.Read()
	if info == (buildinfo.Info{}) {
		return
	}
	logger.Info("dracpu", "goVersion", info.GoVersion, "build", info.VCSRevision, "time", info.VCSTime)
}

func printMetricsMetadata(w io.Writer) error {
	return cpumetrics.WriteJSON(w)
}
