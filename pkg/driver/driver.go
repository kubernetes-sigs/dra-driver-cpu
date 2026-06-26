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
	"time"

	"github.com/containerd/nri/pkg/stub"
	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/ctxlog"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
	cpumetrics "github.com/kubernetes-sigs/dra-driver-cpu/pkg/metrics"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	resourceapi "k8s.io/api/resource/v1"
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
	// GROUP_BY_MACHINE groups CPUs by the entire machine.
	GROUP_BY_MACHINE = "machine"
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
	driverName              string
	nodeName                string
	kubeClient              kubernetes.Interface
	draPlugin               KubeletPlugin
	nriPlugin               stub.Stub
	podConfigStore          *store.PodConfig
	cpuAllocationStore      *store.CPUAllocation
	cdiMgr                  cdiManager
	cpuTopology             *cpuinfo.CPUTopology
	deviceNameToCPUID       map[string]int
	deviceNameToSocketID    map[string]int
	deviceNameToNUMANodeID  map[string]int
	individualDeviceInfos   []cpuDeviceInfo
	groupedDeviceInfos      []groupedCPUDeviceInfo
	deviceSlices            [][]resourceapi.Device
	reservedCPUs            cpuset.CPUSet
	onlineCPUs              cpuset.CPUSet
	cpuDeviceMode           string
	cpuDeviceGroupBy        string
	claimTracker            *store.ClaimTracker
	pcieRootMapper          *store.PCIeRootMapper
	devicesPerResourceSlice int
	metrics                 cpumetrics.Recorder
}

// Providers group the interfaces the CPUDriver depends on
type Providers struct {
	CPUInfo   CPUInfoProvider
	SysFS     device.SysFS
	K8SClient kubernetes.Interface
}

func (pr Providers) EnsureCPUInfo() CPUInfoProvider {
	if pr.CPUInfo == nil {
		return cpuinfo.NewSystemCPUInfo()
	}
	return pr.CPUInfo
}

func (pr Providers) EnsureSysFS() device.SysFS {
	if pr.SysFS == nil {
		return os.DirFS(device.SysfsRoot).(device.SysFS)
	}
	return pr.SysFS
}

// Config is the configuration for the CPUDriver.
type Config struct {
	DriverName       string
	NodeName         string
	ReservedCPUs     cpuset.CPUSet
	CPUDeviceMode    string
	CPUDeviceGroupBy string
	ExposePCIeRoots  bool
	Metrics          cpumetrics.Recorder
}

func (cfg Config) DevicesPerResourceSlice() int {
	if cfg.ExposePCIeRoots {
		// We use the lower "advanced features" limit because the driver
		// may set list-type attributes (StringValues) such as PCIe roots.
		return resourceapi.ResourceSliceMaxDevicesWithAdvancedFeatures
	}
	return resourceapi.ResourceSliceMaxDevices
}

// New creates and initializes a CPUDriver, preparing all internal state.
// No external listeners or goroutines are started; call Start to begin serving.
func New(logger logr.Logger, providers Providers, config *Config) (*CPUDriver, error) {
	logger = logger.WithValues("driver", config.DriverName)

	metricsRecorder := config.Metrics
	if metricsRecorder == nil {
		metricsRecorder = cpumetrics.Noop()
	}
	plugin := &CPUDriver{
		driverName:              config.DriverName,
		nodeName:                config.NodeName,
		kubeClient:              providers.K8SClient,
		deviceNameToCPUID:       make(map[string]int),
		deviceNameToSocketID:    make(map[string]int),
		deviceNameToNUMANodeID:  make(map[string]int),
		reservedCPUs:            config.ReservedCPUs,
		cpuDeviceMode:           config.CPUDeviceMode,
		cpuDeviceGroupBy:        config.CPUDeviceGroupBy,
		claimTracker:            store.NewClaimTracker(),
		pcieRootMapper:          store.NewPCIeRootMapper(),
		devicesPerResourceSlice: config.DevicesPerResourceSlice(),
		metrics:                 metricsRecorder,
	}
	sysfs := providers.EnsureSysFS()

	onlineCPUs, err := cpuinfo.OnlineCPUs(logger, sysfs)
	if err != nil {
		return nil, fmt.Errorf("failed to get online CPUs: %w", err)
	}
	logger.V(2).Info("detected online CPUs", "cpus", onlineCPUs.String())
	plugin.onlineCPUs = onlineCPUs

	topo, err := providers.EnsureCPUInfo().GetCPUTopology(logger)
	if err != nil {
		return nil, fmt.Errorf("failed to get CPU topology: %w", err)
	}
	if topo == nil {
		return nil, fmt.Errorf("failed to get CPU topology: topology is nil")
	}
	plugin.cpuTopology = topo

	if config.ExposePCIeRoots {
		if err := plugin.pcieRootMapper.Probe(logger, sysfs, onlineCPUs); err != nil {
			return nil, fmt.Errorf("failed to list PCIe domains: %w", err)
		}
	}

	plugin.cpuAllocationStore = store.NewCPUAllocation(plugin.cpuTopology, config.ReservedCPUs)
	plugin.refreshAllocationMetrics()
	plugin.podConfigStore = store.NewPodConfig()

	if plugin.cpuDeviceMode == CPU_DEVICE_MODE_GROUPED {
		plugin.groupedDeviceInfos = plugin.groupedCPUDeviceInfos()
		for _, dev := range plugin.groupedDeviceInfos {
			switch plugin.cpuDeviceGroupBy {
			case GROUP_BY_SOCKET:
				plugin.deviceNameToSocketID[dev.name] = dev.socketID
			case GROUP_BY_NUMA_NODE:
				plugin.deviceNameToNUMANodeID[dev.name] = dev.numaNodeID
			}
		}
		plugin.deviceSlices = plugin.createGroupedCPUDeviceSlices(logger)
	} else {
		plugin.individualDeviceInfos = plugin.cpuDeviceInfos()
		for _, dev := range plugin.individualDeviceInfos {
			plugin.deviceNameToCPUID[dev.name] = dev.cpu.CpuID
		}
		plugin.deviceSlices = plugin.createCPUDeviceSlices()
	}

	return plugin, nil
}

// Start registers the plugin with kubelet, starts the NRI plugin, and begins
// async resource publication. Setup must have been called first.
func (cp *CPUDriver) Start(ctx context.Context) (<-chan error, error) {
	_, logger := ctxlog.WithValues(ctx, "driver", cp.driverName)

	asyncErr := make(chan error, 1)

	driverPluginPath := filepath.Join(kubeletPluginPath, cp.driverName)
	if err := os.MkdirAll(driverPluginPath, 0750); err != nil {
		return asyncErr, fmt.Errorf("failed to create plugin path %s: %w", driverPluginPath, err)
	}

	cdiMgr, err := NewCdiManager(logger, cp.driverName, cdiSpecDir)
	if err != nil {
		return asyncErr, fmt.Errorf("failed to create CDI manager: %w", err)
	}
	cp.cdiMgr = cdiMgr

	kubeletOpts := []kubeletplugin.Option{
		kubeletplugin.DriverName(cp.driverName),
		kubeletplugin.NodeName(cp.nodeName),
		kubeletplugin.KubeClient(cp.kubeClient),
	}
	d, err := kubeletplugin.Start(ctx, cp, kubeletOpts...)
	if err != nil {
		return asyncErr, fmt.Errorf("start kubelet plugin: %w", err)
	}
	cp.draPlugin = d
	err = wait.PollUntilContextTimeout(ctx, 1*time.Second, 30*time.Second, true, func(context.Context) (bool, error) {
		status := d.RegistrationStatus()
		if status == nil {
			return false, nil
		}
		return status.PluginRegistered, nil
	})
	if err != nil {
		return asyncErr, err
	}

	// register the NRI plugin
	nriOpts := []stub.Option{
		stub.WithPluginName(cp.driverName),
		stub.WithPluginIdx("00"),
		// https://github.com/containerd/nri/pull/173
		// Otherwise it silently exits the program
		stub.WithOnClose(func() {
			logger.Info("NRI plugin closed")
		}),
	}
	stub, err := stub.New(cp, nriOpts...)
	if err != nil {
		return asyncErr, fmt.Errorf("failed to create plugin stub: %w", err)
	}
	cp.nriPlugin = stub

	go func() {
		if err := runNRIPluginWithRetry(ctx, cp.nriPlugin, maxAttempts); err != nil && ctx.Err() == nil {
			logger.Error(err, "NRI plugin failed to be restarted", "maxAttempts", maxAttempts)
			asyncErr <- err
		}
	}()

	// publish available resources
	go cp.PublishResources(ctx)

	return asyncErr, nil
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
	for i := range maxAttempts {
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
