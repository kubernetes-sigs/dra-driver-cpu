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
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/containerd/nri/pkg/stub"
	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/ctxlog"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/driverconfig"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuallocator"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
	cpumetrics "github.com/kubernetes-sigs/dra-driver-cpu/pkg/metrics"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/store"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/sysfs"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	drametadatav1beta1 "k8s.io/dynamic-resource-allocation/api/metadata/v1beta1"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	registerapi "k8s.io/kubelet/pkg/apis/pluginregistration/v1"
	"k8s.io/utils/cpuset"
)

const (
	// maxAttempts indicates the number of times the driver will try to recover itself before failing
	maxAttempts = 5
	// registrationPollInterval and registrationTimeout bound the wait for kubelet to
	// acknowledge the plugin at startup
	registrationPollInterval = 1 * time.Second
	registrationTimeout      = 30 * time.Second
)

const opIDLen = 8

// KubeletPlugin is an interface that describes the methods used from kubeletplugin.Helper.
type KubeletPlugin interface {
	PublishResources(context.Context, resourceslice.DriverResources) error
	RegistrationStatus() *registerapi.RegistrationStatus
	Stop()
}

type cdiManager interface {
	AddDevice(logger logr.Logger, deviceName string, envVar string) error
	Refresh() error
	GetDeviceEnv(deviceName string) ([]string, error)
	RemoveDevice(logger logr.Logger, deviceName string) error
}

// CPUInfoProvider is an interface for getting CPU information.
type CPUInfoProvider interface {
	GetCPUTopology(logger logr.Logger) (*cpuinfo.CPUTopology, error)
}

// CPUAllocator abstracts the behavior of a cpu allocator code. The caller MUST call
// * Allocate - to get the optimal (according to the backend policy) set of the given size within the available set
// * Validate - to ensure the optimal set returned by Allocate is consistent with in-flight and consolidated allocations
// GetPreferredCPUs is optional and allows the users (or external scheduler) to specify which available CPUs should
// be prioritized in the current allocation request.
// The caller CAN call it, and if it does, it MUST call it first, before Allocate; the caller should then feed the
// preferred CPUs to Allocate(). If the caller doesn't need or want to call GetPreferredCPUs, it must use an empty
// CPU set where preferred CPUs are required.
type CPUAllocator interface {
	// GetPreferredCPUs retrieves the allocation hint for the current allocation cycle.
	// An allocation hint is a CPU set that the allocator must honor: all CPUs allocated in this cycle must belong to
	// that set, or the allocation fails; if there are more preferred CPUs than required, the allocator is free
	// to pick any subset that fulfills the request. If there are less preferred CPUs than required, the allocator
	// is free to pick extra CPUs as it sees fit.
	// The preferred CPU set is per-request. Should the allocation result split across different results (e.g. count > 1),
	// The allocator must ensure that each allocation result is a non-overlapping subset of the preferred set.
	// The allocator can reject any hint entirely by design. If the allocator rejects hint entirely, it must
	// return cpuallocator.ErrUnsupportedPreferredCPUs.
	// If the allocator fails to fetch a hint, or detects an illegal hint, it must return a different custom error.
	GetPreferredCPUs(logger logr.Logger, allocation *resourceapi.AllocationResult, alloc resourceapi.DeviceRequestAllocationResult) (cpuset.CPUSet, error)
	// Allocate reserves <count> CPUs for the claim from the intersection of preferredCPUs and availableCPUs.
	// if preferredCPUs is empty, the allocator is free to pick CPUs from availableCPUs according to its implementation.
	// The allocator can pick CPUs from the provided set according to its own implementation, but each Allocate call must be idempotent.
	Allocate(logger logr.Logger, availableCPUs, preferredCPUs cpuset.CPUSet, count int) (cpuset.CPUSet, error)
	// Validate verifies performed allocation (cpus) is consistent with all the other active
	// claims already prepared on this node (preparedCPUs) and with the other allocations
	// performed in the same batch (assignedCPUs, claim asking for two or more allocations).
	// Returns error detailing the inconsistency, nil if the allocation is correct.
	Validate(cpus, assignedCPUs, preparedCPUs cpuset.CPUSet) error
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
	topology                deviceTopology
	cpuDeviceMode           string
	cpuDeviceGroupBy        string
	claimTracker            *store.ClaimTracker
	pcieRootMapper          *store.PCIeRootMapper
	devicesPerResourceSlice int
	metrics                 cpumetrics.Recorder
	kubeletRootDir          string
	cpuAllocator            CPUAllocator
}

// deviceTopology holds the CPU topology and device-to-CPU/socket/NUMA
// mappings. Set once in New(), read-only after that.
type deviceTopology struct {
	cpuTopology            *cpuinfo.CPUTopology
	deviceNameToCPUID      map[string]int
	deviceNameToSocketID   map[string]int
	deviceNameToNUMANodeID map[string]int
	deviceSlices           [][]resourceapi.Device
	reservedCPUs           cpuset.CPUSet
	onlineCPUs             cpuset.CPUSet
}

// Providers group the interfaces the CPUDriver depends on
type Providers struct {
	CPUInfo   CPUInfoProvider
	SysFS     sysfs.FS
	K8SClient kubernetes.Interface
}

func (pr Providers) EnsureCPUInfo() CPUInfoProvider {
	if pr.CPUInfo == nil {
		return cpuinfo.NewSystemCPUInfo(pr.EnsureSysFS())
	}
	return pr.CPUInfo
}

func (pr Providers) EnsureSysFS() sysfs.FS {
	if pr.SysFS == nil {
		return sysfs.Host()
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
	Allocator        string
	ExposePCIeRoots  bool
	Metrics          cpumetrics.Recorder
	// KubeletRootDir is the kubelet root directory, from which the registrar
	// and plugin data directories are derived. Required and absolute:
	// driverconfig.Resolve refuses an empty or relative value, and New takes it as
	// given rather than checking it again.
	KubeletRootDir string
	// PublishNodeAllocatableResourceMapping publishes KEP-5517 nodeAllocatableResources mappings in
	// ResourceSlice devices. Requires the DRANodeAllocatableResources feature gate to be enabled in the cluster.
	PublishNodeAllocatableResourceMapping bool
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
		driverName: config.DriverName,
		nodeName:   config.NodeName,
		kubeClient: providers.K8SClient,
		topology: deviceTopology{
			deviceNameToCPUID:      make(map[string]int),
			deviceNameToSocketID:   make(map[string]int),
			deviceNameToNUMANodeID: make(map[string]int),
			reservedCPUs:           config.ReservedCPUs,
		},
		cpuDeviceMode:           config.CPUDeviceMode,
		cpuDeviceGroupBy:        config.CPUDeviceGroupBy,
		claimTracker:            store.NewClaimTracker(),
		pcieRootMapper:          store.NewPCIeRootMapper(),
		devicesPerResourceSlice: config.DevicesPerResourceSlice(),
		metrics:                 metricsRecorder,
		kubeletRootDir:          config.KubeletRootDir,
	}
	sfs := providers.EnsureSysFS()

	onlineCPUs, err := cpuinfo.OnlineCPUs(logger, sfs)
	if err != nil {
		return nil, fmt.Errorf("failed to get online CPUs: %w", err)
	}
	logger.V(2).Info("detected online CPUs", "cpus", onlineCPUs.String())
	plugin.topology.onlineCPUs = onlineCPUs

	topo, err := providers.EnsureCPUInfo().GetCPUTopology(logger)
	if err != nil {
		return nil, fmt.Errorf("failed to get CPU topology: %w", err)
	}
	if topo == nil {
		return nil, fmt.Errorf("failed to get CPU topology: topology is nil")
	}
	plugin.topology.cpuTopology = topo

	if config.ExposePCIeRoots {
		if err := plugin.pcieRootMapper.Probe(logger, sfs, onlineCPUs); err != nil {
			return nil, fmt.Errorf("failed to list PCIe domains: %w", err)
		}
	}

	plugin.cpuAllocationStore = store.NewCPUAllocation(plugin.topology.cpuTopology, config.ReservedCPUs)
	plugin.refreshAllocationMetrics()
	plugin.podConfigStore = store.NewPodConfig()

	allocMode := findAllocatorMode(logger, config)
	logger.Info("creating CPU allocator", "method", allocMode)
	switch allocMode {
	case driverconfig.AllocatorExternal:
		plugin.cpuAllocator = cpuallocator.NewExternal(config.DriverName, topo, plugin.topology.onlineCPUs, config.ReservedCPUs)
	default:
		plugin.cpuAllocator = cpuallocator.NewKubelet(config.DriverName, topo)
	}

	var devices []resourceapi.Device

	if plugin.cpuDeviceMode == device.CPU_DEVICE_MODE_GROUPED {
		var nameToID map[string]int
		devices, nameToID = device.BuildGrouped(logger, plugin.cpuDeviceGroupBy, plugin.topology.cpuTopology, plugin.topology.onlineCPUs, plugin.topology.reservedCPUs, plugin.pcieRootMapper, config.PublishNodeAllocatableResourceMapping)
		switch plugin.cpuDeviceGroupBy {
		case device.GROUP_BY_SOCKET:
			plugin.topology.deviceNameToSocketID = nameToID
		case device.GROUP_BY_NUMA_NODE:
			plugin.topology.deviceNameToNUMANodeID = nameToID
		}
	} else {
		devices, plugin.topology.deviceNameToCPUID = device.Build(plugin.topology.cpuTopology, plugin.topology.reservedCPUs, plugin.pcieRootMapper, config.PublishNodeAllocatableResourceMapping)
	}

	if len(devices) > 0 {
		// Chunk devices into slices of at most devicesPerResourceSlice
		plugin.topology.deviceSlices = slices.Collect(slices.Chunk(devices, plugin.devicesPerResourceSlice))
	}
	return plugin, nil
}

// registrarDir is the kubelet plugin registration directory, always
// <kubelet-root>/plugins_registry.
func registrarDir(kubeletRootDir string) string {
	return filepath.Join(kubeletRootDir, "plugins_registry")
}

// pluginDataDir is the per-driver directory where the DRA socket is created. It
// includes the driver name because the kubeletplugin contract requires it not
// to be shared with other kubelet plugins.
func pluginDataDir(kubeletRootDir, driverName string) string {
	return filepath.Join(kubeletRootDir, "plugins", driverName)
}

// unixPathMax is the longest pathname a Unix domain socket can be bound to:
// sun_path is 108 bytes and the kernel needs the terminating NUL.
const unixPathMax = 107

// checkSocketPathFits rejects a kubelet root that leaves no room for the socket
// the kubeletplugin helper binds underneath it. The registrar path is the longer
// of the two the helper binds, so checking it covers both.
//
// Not in Config.Validate with the root's other checks because the length depends
// on the driver name, which the config does not carry. sun_path is a byte
// buffer, so this counts bytes rather than characters.
//
// The name is <driver>-reg.sock while rolling updates are off. Turning them on
// puts the UID in it, which this budget and the chart's both have to follow, and
// RegistrarSocketFilename cannot pin the name back because the two options are
// mutually exclusive.
func checkSocketPathFits(kubeletRootDir, driverName string) error {
	socket := filepath.Join(registrarDir(kubeletRootDir), driverName+"-reg.sock")
	if len(socket) > unixPathMax {
		return fmt.Errorf("kubelet registrar socket path %q is %d bytes, over the %d-byte limit for a Unix socket path: kubeletRootDir has %d bytes to spend and is using %d",
			socket, len(socket), unixPathMax,
			unixPathMax-(len(socket)-len(kubeletRootDir)), len(kubeletRootDir))
	}
	return nil
}

// Start registers the plugin with kubelet, starts the NRI plugin, and begins
// async resource publication. Setup must have been called first.
func (cp *CPUDriver) Start(ctx context.Context) (<-chan error, error) {
	ctx, logger := ctxlog.WithValues(ctx, "driver", cp.driverName)

	asyncErr := make(chan error, 1)

	if err := checkSocketPathFits(cp.kubeletRootDir, cp.driverName); err != nil {
		return asyncErr, err
	}

	driverPluginPath := pluginDataDir(cp.kubeletRootDir, cp.driverName)
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
		kubeletplugin.RegistrarDirectoryPath(registrarDir(cp.kubeletRootDir)),
		kubeletplugin.PluginDataDirectoryPath(driverPluginPath),
		kubeletplugin.EnableDeviceMetadata(true, []schema.GroupVersion{drametadatav1beta1.SchemeGroupVersion}),
	}
	d, err := kubeletplugin.Start(ctx, cp, kubeletOpts...)
	if err != nil {
		return asyncErr, fmt.Errorf("start kubelet plugin: %w", err)
	}
	cp.draPlugin = d
	if err := waitForRegistration(ctx, d, registrarDir(cp.kubeletRootDir), registrationPollInterval, registrationTimeout); err != nil {
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

func getDeviceAttributes(deviceSlices [][]resourceapi.Device, deviceName string) (map[resourceapi.QualifiedName]resourceapi.DeviceAttribute, bool) {
	for _, slice := range deviceSlices {
		for _, dev := range slice {
			if dev.Name == deviceName {
				return dev.Attributes, true
			}
		}
	}
	return nil, false
}

// Shutdown is called when the runtime is shutting down.
func (cp *CPUDriver) Shutdown(ctx context.Context) {
	logger := ctxlog.FromContext(ctx)
	logger.Info("runtime shutting down")
}

// waitForRegistration waits for kubelet to report the plugin as registered. On timeout
// it reports the last reason kubelet gave for refusing, or that it never reported at all.
func waitForRegistration(ctx context.Context, p KubeletPlugin, registrarPath string, interval, timeout time.Duration) error {
	logger := ctxlog.FromContext(ctx)
	var lastRejection string
	var sawStatus bool
	err := wait.PollUntilContextTimeout(ctx, interval, timeout, true, func(context.Context) (bool, error) {
		status := p.RegistrationStatus()
		if status == nil {
			return false, nil
		}
		sawStatus = true
		// Kubelet retries from scratch, so keep the newest reason but do not stop here.
		// Only the newest survives to the error, so an earlier one is logged instead of lost.
		if status.Error != "" && status.Error != lastRejection {
			if lastRejection != "" {
				logger.Info("kubelet gave a new reason for refusing the plugin", "previous", lastRejection, "current", status.Error)
			}
			lastRejection = status.Error
		}
		return status.PluginRegistered, nil
	})
	// Only a timeout is worth explaining; a cancelled context is the caller shutting down.
	if !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	switch {
	case lastRejection != "":
		return fmt.Errorf("kubelet did not register the plugin, last rejection was %q: %w", lastRejection, err)
	case sawStatus:
		return fmt.Errorf("kubelet did not register the plugin and reported no reason: %w", err)
	default:
		return fmt.Errorf("kubelet never reported a registration status, check that it watches %s: %w", registrarPath, err)
	}
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

func findAllocatorMode(logger logr.Logger, config *Config) string {
	if config.Allocator == driverconfig.AllocatorExternal {
		// easy case: explicit user preference
		return config.Allocator
	}
	if config.CPUDeviceMode == device.CPU_DEVICE_MODE_GROUPED && config.CPUDeviceGroupBy == device.GROUP_BY_MACHINE {
		logger.Info("machine grouping in grouped device mode requires external allocator, forcing")
		return driverconfig.AllocatorExternal
	}
	return driverconfig.AllocatorCPUManager
}
