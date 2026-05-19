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
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/containerd/nri/pkg/stub"
	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/ctxlog"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	"k8s.io/utils/cpuset"
)

const (
	// CPU_DEVICE_MODE_GROUPED exposes a single device for a group of CPUs.
	CPU_DEVICE_MODE_GROUPED = "grouped"
	// CPU_DEVICE_MODE_INDIVIDUAL exposes each CPU as a separate device.
	CPU_DEVICE_MODE_INDIVIDUAL = "individual"
)

const (
	// GROUP_BY_SOCKET groups CPUs by socket.
	GROUP_BY_SOCKET = "socket"
	// GROUP_BY_NUMA_NODE groups CPUs by NUMA node.
	GROUP_BY_NUMA_NODE = "numanode"
	// GROUP_BY_PCIE_ROOT groups CPUs by PCIe Root locality.
	GROUP_BY_PCIE_ROOT = "pcieroot"
)

const (
	kubeletPluginPath = "/var/lib/kubelet/plugins"
	// maxAttempts indicates the number of times the driver will try to recover itself before failing
	maxAttempts = 5
)

const opIDLen = 8

// KubeletPlugin is an interface that describes the methods used from kubeletplugin.Helper.
type KubeletPlugin interface {
	PublishResources(context.Context, resourceslice.DriverResources) error
	Stop()
}

type cdiManager interface {
	AddDevice(logger logr.Logger, deviceName string, envVar string) error
	RemoveDevice(logger logr.Logger, deviceName string) error
}

// CPUInfoProvider is an interface for getting CPU information.
// TODO(pravk03): This interface can be simplified. We can export only GetCPUTopology() and remove GetCPUInfos().
type CPUInfoProvider interface {
	GetCPUInfos(logger logr.Logger) ([]cpuinfo.CPUInfo, error)
	GetCPUTopology(logger logr.Logger) (*cpuinfo.CPUTopology, error)
}

// CPUDriver is the structure that holds all the driver runtime information.
type CPUDriver struct {
	driverName             string
	nodeName               string
	kubeClient             kubernetes.Interface
	draPlugin              KubeletPlugin
	nriPlugin              stub.Stub
	podConfigStore         *store.PodConfig
	cpuAllocationStore     *store.CPUAllocation
	cdiMgr                 cdiManager
	cpuTopology            *cpuinfo.CPUTopology
	pcieDomains            []device.PCIeDomain
	deviceNameToCPUID      map[string]int
	deviceNameToSocketID   map[string]int
	deviceNameToNUMANodeID map[string]int
	reservedCPUs           cpuset.CPUSet
	cpuDeviceMode          string
	cpuDeviceGroupBy       string
	claimTracker           *store.ClaimTracker
	deviceNameToPCIeRoot   map[string]*device.PCIeDomain
	cpuIDToPCIeDomain      map[int]*device.PCIeDomain
}

// Config is the configuration for the CPUDriver.
type Config struct {
	DriverName       string
	NodeName         string
	ReservedCPUs     cpuset.CPUSet
	CPUDeviceMode    string
	CPUDeviceGroupBy string
}

// Start creates and starts a new CPUDriver.
func Start(ctx context.Context, clientset kubernetes.Interface, config *Config) (*CPUDriver, <-chan error, error) {
	asyncErr := make(chan error, 1)

	if config.CPUDeviceGroupBy == GROUP_BY_PCIE_ROOT && runtime.GOARCH != "amd64" {
		return nil, asyncErr, fmt.Errorf("PCIe root grouping is not supported on %s, only on x86_64", runtime.GOARCH)
	}

	var logger logr.Logger
	ctx, logger = ctxlog.WithValues(ctx, "driver", config.DriverName)

	plugin := &CPUDriver{
		driverName:             config.DriverName,
		nodeName:               config.NodeName,
		kubeClient:             clientset,
		deviceNameToCPUID:      make(map[string]int),
		deviceNameToSocketID:   make(map[string]int),
		deviceNameToNUMANodeID: make(map[string]int),
		deviceNameToPCIeRoot:   make(map[string]*device.PCIeDomain),
		cpuIDToPCIeDomain:      make(map[int]*device.PCIeDomain),
		reservedCPUs:           config.ReservedCPUs,
		cpuDeviceMode:          config.CPUDeviceMode,
		cpuDeviceGroupBy:       config.CPUDeviceGroupBy,
		claimTracker:           store.NewClaimTracker(),
	}
	sysfs := os.DirFS(device.SysfsRoot).(device.SysFS)

	onlineCPUs, err := device.OnlineCPUs(logger, sysfs)
	if err != nil {
		return nil, asyncErr, fmt.Errorf("failed to get online CPUs: %w", err)
	}
	logger.Info("detected online CPUs", "cpus", onlineCPUs.String())

	cpuInfoProvider := cpuinfo.NewSystemCPUInfo()
	topo, err := cpuInfoProvider.GetCPUTopology(logger)
	if err != nil {
		return nil, asyncErr, fmt.Errorf("failed to get CPU topology: %w", err)
	}
	if topo == nil {
		return nil, asyncErr, fmt.Errorf("failed to get CPU topology: topology is nil")
	}
	plugin.cpuTopology = topo

	plugin.pcieDomains, err = device.PCIeDomainsFromFS(logger, sysfs)
	if err != nil {
		return nil, asyncErr, fmt.Errorf("failed to list PCIe domains: %w", err)
	}
	if len(plugin.pcieDomains) > 0 {
		logger.Info("PCIe domains: detected from the system", "count", len(plugin.pcieDomains))
	} else {
		logger.Info("PCIe domains: none detected, device attributes will not be available")
	}

	extraCPUs := device.FindOrphanedCPUs(plugin.pcieDomains, onlineCPUs)
	if !extraCPUs.IsEmpty() {
		return nil, asyncErr, fmt.Errorf("found cpus not local to any detected PCIe Root: %s", extraCPUs.String())
	}

	plugin.cpuIDToPCIeDomain = device.MapCPUsToPCIeDomain(plugin.pcieDomains, onlineCPUs)
	logger.Info("mapped CPUs to PCIe domains", "count", len(plugin.cpuIDToPCIeDomain))

	plugin.cpuAllocationStore = store.NewCPUAllocation(plugin.cpuTopology, config.ReservedCPUs)
	plugin.podConfigStore = store.NewPodConfig()

	driverPluginPath := filepath.Join(kubeletPluginPath, config.DriverName)
	if err := os.MkdirAll(driverPluginPath, 0750); err != nil {
		return nil, asyncErr, fmt.Errorf("failed to create plugin path %s: %w", driverPluginPath, err)
	}

	kubeletOpts := []kubeletplugin.Option{
		kubeletplugin.DriverName(config.DriverName),
		kubeletplugin.NodeName(config.NodeName),
		kubeletplugin.KubeClient(clientset),
	}
	d, err := kubeletplugin.Start(ctx, plugin, kubeletOpts...)
	if err != nil {
		return nil, asyncErr, fmt.Errorf("start kubelet plugin: %w", err)
	}
	plugin.draPlugin = d
	err = wait.PollUntilContextTimeout(ctx, 1*time.Second, 30*time.Second, true, func(context.Context) (bool, error) {
		status := d.RegistrationStatus()
		if status == nil {
			return false, nil
		}
		return status.PluginRegistered, nil
	})
	if err != nil {
		return nil, asyncErr, err
	}

	cdiMgr, err := NewCdiManager(logger, config.DriverName)
	if err != nil {
		return nil, asyncErr, fmt.Errorf("failed to create CDI manager: %w", err)
	}
	plugin.cdiMgr = cdiMgr

	// register the NRI plugin
	nriOpts := []stub.Option{
		stub.WithPluginName(config.DriverName),
		stub.WithPluginIdx("00"),
		// https://github.com/containerd/nri/pull/173
		// Otherwise it silently exits the program
		stub.WithOnClose(func() {
			logger.Info("NRI plugin closed")
		}),
	}
	stub, err := stub.New(plugin, nriOpts...)
	if err != nil {
		return nil, asyncErr, fmt.Errorf("failed to create plugin stub: %w", err)
	}
	plugin.nriPlugin = stub

	go func() {
		if err := runNRIPluginWithRetry(ctx, plugin.nriPlugin, maxAttempts); err != nil && ctx.Err() == nil {
			logger.Error(err, "NRI plugin failed to be restarted", "maxAttempts", maxAttempts)
			asyncErr <- err
		}
	}()

	// publish available resources
	go plugin.PublishResources(ctx)

	return plugin, asyncErr, nil
}

// Stop stops the CPUDriver.
func (cp *CPUDriver) Stop() {
	cp.nriPlugin.Stop()
	cp.draPlugin.Stop()
}

// Shutdown is called when the runtime is shutting down.
func (cp *CPUDriver) Shutdown(ctx context.Context) {
	logger := ctxlog.FromContext(ctx)
	logger.Info("runtime shutting down")
}

type nriRunner interface {
	Run(context.Context) error
}

func runNRIPluginWithRetry(ctx context.Context, plugin nriRunner, maxAttempts int) error {
	logger := ctxlog.FromContext(ctx)
	for i := 0; i < maxAttempts; i++ {
		err := plugin.Run(ctx)
		if ctx.Err() != nil {
			logger.Info("NRI plugin stopped", "reason", "context cancelled")
			return ctx.Err()
		}
		if err != nil {
			logger.Error(err, "NRI plugin failed, restarting", "attempt", i+1, "maxAttempts", maxAttempts)
		}
	}
	return fmt.Errorf("NRI plugin failed for %d times to be restarted", maxAttempts)
}

// generateShortID generates a non-crypto safe unique ID in cases on which a full UUID would be a overkill.
func generateShortID(length int) string {
	const hexDigits = "0123456789abcdef"
	b := make([]byte, length)
	for i := range b {
		b[i] = hexDigits[rand.IntN(len(hexDigits))] //nolint:gosec
	}
	return string(b)
}
