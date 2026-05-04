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
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/driver"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sys/unix"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	nodeutil "k8s.io/component-helpers/node/util"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/textlogger"
	"k8s.io/utils/cpuset"
)

const (
	driverName = "dra.cpu"
)

var (
	hostnameOverride string
	kubeconfig       string
	bindAddress      string
	reservedCPUs     string
	ready            atomic.Bool
	cpuDeviceMode    string
	groupBy          string
)

type cpuDeviceModeValue struct {
	value *string
}

func newCPUDeviceModeValue(val *string, def string) *cpuDeviceModeValue {
	*val = def
	return &cpuDeviceModeValue{value: val}
}

func (v *cpuDeviceModeValue) String() string {
	if v == nil || v.value == nil {
		return ""
	}
	return *v.value
}

func (v *cpuDeviceModeValue) Set(s string) error {
	if s != driver.CPU_DEVICE_MODE_GROUPED && s != driver.CPU_DEVICE_MODE_INDIVIDUAL {
		return fmt.Errorf("invalid value: %q, must be %s or %s", s, driver.CPU_DEVICE_MODE_GROUPED, driver.CPU_DEVICE_MODE_INDIVIDUAL)
	}
	*v.value = s
	return nil
}

type groupByValue struct {
	value *string
}

func newGroupByValue(val *string, def string) *groupByValue {
	*val = def
	return &groupByValue{value: val}
}

func (v *groupByValue) String() string {
	if v == nil || v.value == nil {
		return ""
	}
	return *v.value
}

func (v *groupByValue) Set(s string) error {
	if s != driver.GROUP_BY_SOCKET && s != driver.GROUP_BY_NUMA_NODE {
		return fmt.Errorf("invalid value: %q, must be %s or %s", s, driver.GROUP_BY_SOCKET, driver.GROUP_BY_NUMA_NODE)
	}
	*v.value = s
	return nil
}

func init() {
	flag.StringVar(&kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	flag.StringVar(&hostnameOverride, "hostname-override", "", "If non-empty, will be used as the name of the Node that kube-network-policies is running on. If unset, the node name is assumed to be the same as the node's hostname.")
	flag.StringVar(&bindAddress, "bind-address", ":8080", "The address to bind the HTTP server for /healthz and /metrics endpoints")
	flag.StringVar(&reservedCPUs, "reserved-cpus", "", "cpuset of CPUs to be excluded from ResourceSlice.")
	flag.Var(newCPUDeviceModeValue(&cpuDeviceMode, driver.CPU_DEVICE_MODE_GROUPED), "cpu-device-mode", "Sets the mode for exposing CPU devices. 'grouped' exposes a single device per socket or numa node (based on --group-by). 'individual' exposes each CPU as a separate device.")
	flag.Var(newGroupByValue(&groupBy, driver.GROUP_BY_NUMA_NODE), "group-by", "When --cpu-device-mode=grouped, sets the criteria for grouping CPUs. Can be set to 'socket' or 'numanode'.")
}

func main() {
	config := textlogger.NewConfig()
	config.AddFlags(flag.CommandLine)
	flag.Parse()

	logger := textlogger.NewLogger(config)
	// some key deps still call klog directly, so we need this integration.
	// TODO: check every time we bump kube libs to a new major version,
	// as the contextual logging transition to kube libs is still ongoing
	// k8s.io/client-go
	// k8s.io/apimachinery
	// k8s.io/dynamic-resource-allocation
	// k8s.io/kube-openapi
	// k8s.io/utils
	// k8s.io/component-helpers
	// k8s.io/kubelet
	klog.SetLoggerWithOptions(logger, klog.ContextualLogger(true))

	if err := run(logger); err != nil {
		logger.Error(err, "failed to run")
		os.Exit(1)
	}
}

func run(logger logr.Logger) error {
	printVersion(logger)
	flag.VisitAll(func(f *flag.Flag) {
		logger.Info("FLAG", "name", f.Name, "value", f.Value.String())
	})

	reservedCPUSet, err := cpuset.Parse(reservedCPUs)
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
		Addr:              bindAddress,
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
	if kubeconfig != "" {
		restConfig, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
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

	nodeName, err := nodeutil.GetHostname(hostnameOverride)
	if err != nil {
		return fmt.Errorf("can not obtain the node name, use the hostname-override flag if you want to set it to a specific value: %w", err)
	}

	// trap Ctrl+C and call cancel on the context
	ctx := klog.NewContext(context.Background(), logger)
	ctx, cancel := context.WithCancel(ctx)

	// Enable signal handler
	signalCh := make(chan os.Signal, 2)
	defer func() {
		close(signalCh)
		cancel()
	}()
	signal.Notify(signalCh, os.Interrupt, unix.SIGINT)

	driverConfig := &driver.Config{
		DriverName:       driverName,
		NodeName:         nodeName,
		ReservedCPUs:     reservedCPUSet,
		CpuDeviceMode:    cpuDeviceMode,
		CPUDeviceGroupBy: groupBy,
	}
	dracpu, asyncErr, err := driver.Start(ctx, clientset, driverConfig)
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
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return
	}
	var vcsRevision, vcsTime string
	for _, f := range info.Settings {
		switch f.Key {
		case "vcs.revision":
			vcsRevision = f.Value
		case "vcs.time":
			vcsTime = f.Value
		}
	}
	logger.Info("dracpu", "goVersion", info.GoVersion, "build", vcsRevision, "time", vcsTime)
}
