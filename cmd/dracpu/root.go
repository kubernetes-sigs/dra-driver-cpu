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
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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

var rootCmd = &cobra.Command{
	Use:           "dracpu",
	Short:         "DRA CPU driver for Kubernetes",
	Long:          `Dynamic Resource Allocation (DRA) driver for CPU resources in Kubernetes clusters.`,
	RunE:          runDriver,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	klog.InitFlags(nil)
	rootCmd.Flags().AddGoFlagSet(flag.CommandLine)

	rootCmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "absolute path to the kubeconfig file")
	rootCmd.Flags().StringVar(&hostnameOverride, "hostname-override", "", "If non-empty, will be used as the name of the Node that kube-network-policies is running on. If unset, the node name is assumed to be the same as the node's hostname.")
	rootCmd.Flags().StringVar(&bindAddress, "bind-address", ":8080", "The address to bind the HTTP server for /healthz and /metrics endpoints")
	rootCmd.Flags().StringVar(&reservedCPUs, "reserved-cpus", "", "cpuset of CPUs to be excluded from ResourceSlice.")
	rootCmd.Flags().StringVar(&cpuDeviceMode, "cpu-device-mode", driver.CPU_DEVICE_MODE_GROUPED, "Sets the mode for exposing CPU devices. 'grouped' exposes a single device per socket or numa node (based on --group-by). 'individual' exposes each CPU as a separate device.")
	rootCmd.Flags().StringVar(&groupBy, "group-by", driver.GROUP_BY_NUMA_NODE, "When --cpu-device-mode=grouped, sets the criteria for grouping CPUs. Can be set to 'socket' or 'numanode'.")
}

func runDriver(cmd *cobra.Command, args []string) error {
	printVersion()

	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		klog.Infof("FLAG: --%s=%q", f.Name, f.Value)
	})

	if cpuDeviceMode != driver.CPU_DEVICE_MODE_GROUPED && cpuDeviceMode != driver.CPU_DEVICE_MODE_INDIVIDUAL {
		return fmt.Errorf("invalid --cpu-device-mode: %q, must be %s or %s", cpuDeviceMode, driver.CPU_DEVICE_MODE_GROUPED, driver.CPU_DEVICE_MODE_INDIVIDUAL)
	}

	if groupBy != driver.GROUP_BY_SOCKET && groupBy != driver.GROUP_BY_NUMA_NODE {
		return fmt.Errorf("invalid --group-by: %q, must be %s or %s", groupBy, driver.GROUP_BY_SOCKET, driver.GROUP_BY_NUMA_NODE)
	}

	reservedCPUSet, err := cpuset.Parse(reservedCPUs)
	if err != nil {
		return fmt.Errorf("failed to parse reserved CPUs: %w", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		} else {
			w.WriteHeader(http.StatusOK)
		}
	})
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
		config, err = rest.InClusterConfig()
	}
	if err != nil {
		return fmt.Errorf("can not create client-go configuration: %w", err)
	}

	config.AcceptContentTypes = "application/vnd.kubernetes.protobuf,application/json"
	config.ContentType = "application/vnd.kubernetes.protobuf"

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("can not create client-go client: %w", err)
	}

	nodeName, err := nodeutil.GetHostname(hostnameOverride)
	if err != nil {
		return fmt.Errorf("can not obtain the node name, use the hostname-override flag if you want to set it to a specific value: %w", err)
	}

	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)

	signalCh := make(chan os.Signal, 2)
	defer func() {
		close(signalCh)
		cancel()
	}()
	signal.Notify(signalCh, os.Interrupt, unix.SIGINT, unix.SIGTERM)

	driverConfig := &driver.Config{
		DriverName:       driverName,
		NodeName:         nodeName,
		ReservedCPUs:     reservedCPUSet,
		CpuDeviceMode:    cpuDeviceMode,
		CPUDeviceGroupBy: groupBy,
	}
	dracpu, err := driver.Start(ctx, clientset, driverConfig)
	if err != nil {
		return fmt.Errorf("driver failed to start: %w", err)
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

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		klog.Errorf("HTTP server shutdown failed: %v", err)
	}

	return nil
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
