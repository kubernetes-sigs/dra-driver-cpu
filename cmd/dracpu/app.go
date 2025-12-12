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
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"sync/atomic"
	"time"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/driver"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"golang.org/x/sys/unix"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	nodeutil "k8s.io/component-helpers/node/util"
	"k8s.io/klog/v2"
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
	klog.InitFlags(nil)
	flag.Parse()

	printVersion()
	flag.VisitAll(func(f *flag.Flag) {
		klog.Infof("FLAG: --%s=%q", f.Name, f.Value)
	})

	reservedCPUSet, err := cpuset.Parse(reservedCPUs)
	if err != nil {
		klog.Fatalf("failed to parse reserved CPUs: %v", err)
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
			klog.Errorf("HTTP server failed: %v", err)
		}
	}()

	var config *rest.Config
	if kubeconfig != "" {
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		// creates the in-cluster config
		config, err = rest.InClusterConfig()
	}
	if err != nil {
		klog.Fatalf("can not create client-go configuration: %v", err)
	}

	// use protobuf for better performance at scale
	// https://kubernetes.io/docs/reference/using-api/api-concepts/#alternate-representations-of-resources
	config.AcceptContentTypes = "application/vnd.kubernetes.protobuf,application/json"
	config.ContentType = "application/vnd.kubernetes.protobuf"

	// creates the clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		klog.Fatalf("can not create client-go client: %v", err)
	}

	nodeName, err := nodeutil.GetHostname(hostnameOverride)
	if err != nil {
		klog.Fatalf("can not obtain the node name, use the hostname-override flag if you want to set it to a specific value: %v", err)
	}

	// trap Ctrl+C and call cancel on the context
	ctx := context.Background()
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
	dracpu, err := driver.Start(ctx, clientset, driverConfig)
	if err != nil {
		klog.Fatalf("driver failed to start: %v", err)
	}
	defer dracpu.Stop()
	ready.Store(true)
	klog.Info("driver started")

	select {
	case <-signalCh:
		klog.Infof("Exiting: received signal")
		cancel()
	case <-ctx.Done():
		klog.Infof("Exiting: context cancelled")
	}

	// Gracefully shutdown HTTP server
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		klog.Errorf("HTTP server shutdown failed: %v", err)
	}
}

func printVersion() {
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
	klog.Infof("dracpu go %s build: %s time: %s", info.GoVersion, vcsRevision, vcsTime)
}
