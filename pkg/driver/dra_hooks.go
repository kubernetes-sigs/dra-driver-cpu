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
	"os"
	"time"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/ctxlog"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuallocator"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
	cpumetrics "github.com/kubernetes-sigs/dra-driver-cpu/pkg/metrics"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/dynamic-resource-allocation/resourceslice"
	"k8s.io/utils/cpuset"
	cdiparser "tags.cncf.io/container-device-interface/pkg/parser"
)

// PublishResources publishes ResourceSlice for CPU resources.
func (cp *CPUDriver) PublishResources(ctx context.Context) {
	ctx, logger := ctxlog.WithValues(ctx, "opID", generateShortID(opIDLen), "deviceMode", cp.cpuDeviceMode, "groupBy", cp.cpuDeviceGroupBy)

	logger.V(4).Info("begin: publishing resources")
	defer logger.V(4).Info("end: publishing resources")

	if cp.topology.deviceSlices == nil {
		logger.Info("no devices to publish or error occurred")
		return
	}

	slices := make([]resourceslice.Slice, 0, len(cp.topology.deviceSlices))
	for _, chunk := range cp.topology.deviceSlices {
		slices = append(slices, resourceslice.Slice{Devices: chunk})
	}

	resources := resourceslice.DriverResources{
		Pools: map[string]resourceslice.Pool{
			// All slices are published under the same pool for this node.
			cp.nodeName: {Slices: slices},
		},
	}

	err := cp.draPlugin.PublishResources(ctx, resources)
	if err != nil {
		logger.Error(err, "error publishing resources")
	}
}

// PrepareResourceClaims is called by the kubelet to prepare a resource claim.
func (cp *CPUDriver) PrepareResourceClaims(ctx context.Context, claims []*resourceapi.ResourceClaim) (map[types.UID]kubeletplugin.PrepareResult, error) {
	_, logger := ctxlog.WithValues(ctx, "opID", generateShortID(opIDLen))

	logger.V(4).Info("begin: preparing resource claims", "numClaims", len(claims))
	defer logger.V(4).Info("end: preparing resource claims", "numClaims", len(claims))

	result := make(map[types.UID]kubeletplugin.PrepareResult)

	if len(claims) == 0 {
		return result, nil
	}

	for _, claim := range claims {
		start := time.Now()
		cLogger := logger.WithValues("claim", ctxlog.KObj(claim), "claimUID", claim.UID)
		if cp.cpuDeviceMode == device.CPU_DEVICE_MODE_GROUPED {
			result[claim.UID] = cp.prepareGroupedResourceClaim(cLogger, claim)
		} else {
			result[claim.UID] = cp.prepareResourceClaim(cLogger, claim)
		}
		prepareResult := cpumetrics.ResultSuccess
		if result[claim.UID].Err != nil {
			prepareResult = cpumetrics.ResultError
		}
		cp.metricsRecorder().RecordPrepare(prepareResult, time.Since(start))
	}
	return result, nil
}

func getCDIDeviceName(uid types.UID) string {
	return fmt.Sprintf("claim-%s", uid)
}

// reserveResourceClaimAllocation records a new claim allocation while applying
// the shared-pool guard for currently running shared containers. A shared
// container from the same pod may not have been created yet when this DRA hook
// runs, so that case is detected later by the NRI CreateContainer check.
func (cp *CPUDriver) reserveResourceClaimAllocation(logger logr.Logger, claimUID types.UID, cpus cpuset.CPUSet) error {
	hasSharedContainers := len(cp.podConfigStore.GetContainersWithSharedCPUs()) > 0
	return cp.cpuAllocationStore.ReserveResourceClaimAllocation(logger, claimUID, cpus, hasSharedContainers)
}

func (cp *CPUDriver) prepareGroupedResourceClaim(logger logr.Logger, claim *resourceapi.ResourceClaim) kubeletplugin.PrepareResult {
	logger.V(4).Info("preparing grouped resource claim")

	if claim.Status.Allocation == nil {
		return kubeletplugin.PrepareResult{
			Err: fmt.Errorf("claim %s/%s has no allocation", claim.Namespace, claim.Name),
		}
	}

	if existingCPUs, ok := cp.cpuAllocationStore.GetResourceClaimAllocation(claim.UID); ok {
		logger.V(2).Info("claim already has allocated CPUs in store, reusing assignment", "cpus", existingCPUs.String())
		// Even if the claim is already allocated in our in-memory store (which happens when a duplicate prepare
		// call is invoked without an intermediate unprepare), we must call prepareDevices and return the result back to Kubelet.
		// If the CDI file is already created on disk, the CDI manager will safely overwrite it with the same configuration.
		// This ensures that the CDI specification file is written/recreated on disk (for example, if the driver
		// pod restarted and synchronized its memory store from the runtime but did not recreate the CDI files on disk).
		return cp.prepareDevices(logger, claim, existingCPUs)
	}

	var assignedCPUs cpuset.CPUSet
	allocatableCPUs := cp.cpuAllocationStore.GetSharedCPUs()

	for _, alloc := range claim.Status.Allocation.Devices.Results {
		if alloc.Driver != cp.driverName {
			continue
		}
		quantity, ok := alloc.ConsumedCapacity[device.CPUResourceQualifiedName]
		if !ok {
			return kubeletplugin.PrepareResult{Err: fmt.Errorf("CPU capacity %q for device %q is missing", device.CPUResourceQualifiedName, alloc.Device)}
		}
		if quantity.Sign() <= 0 {
			return kubeletplugin.PrepareResult{Err: fmt.Errorf("CPU capacity for device %q must be positive, got %s", alloc.Device, quantity.String())}
		}
		count := quantity.Value()
		if quantity.CmpInt64(count) != 0 {
			return kubeletplugin.PrepareResult{Err: fmt.Errorf("CPU capacity for device %q must be a whole number, got %s", alloc.Device, quantity.String())}
		}

		claimCPUCount := int(count)
		logger.V(4).Info("found CPU request", "numCPUs", claimCPUCount, "device", alloc.Device)

		topo := cp.topology.cpuTopology
		// TODO: what if `claimCPUCount==0`?

		var cur cpuset.CPUSet
		preferredCPUs, err := cp.cpuAllocator.GetPreferredCPUs(logger, claim.Status.Allocation, alloc)
		if err != nil {
			return kubeletplugin.PrepareResult{Err: err}
		}

		switch cp.cpuDeviceGroupBy {
		case device.GROUP_BY_SOCKET:
			socketID, ok := cp.topology.deviceNameToSocketID[alloc.Device]
			if !ok {
				return kubeletplugin.PrepareResult{Err: fmt.Errorf("no valid socket ID found for device %s", alloc.Device)}
			}
			socketCPUs := topo.CPUDetails.CPUsInSockets(socketID)
			availableCPUsForDevice := allocatableCPUs.Difference(assignedCPUs).Intersection(socketCPUs)
			logger.V(4).Info("socket CPU availability", "socketID", socketID, "socketCPUs", socketCPUs.String(), "availableCPUs", availableCPUsForDevice.String())
			cur, err = cp.cpuAllocator.Allocate(logger, availableCPUsForDevice, preferredCPUs, claimCPUCount)
		case device.GROUP_BY_NUMA_NODE:
			numaNodeID, ok := cp.topology.deviceNameToNUMANodeID[alloc.Device]
			if !ok {
				return kubeletplugin.PrepareResult{Err: fmt.Errorf("no valid NUMA node ID found for device %s", alloc.Device)}
			}
			numaCPUs := topo.CPUDetails.CPUsInNUMANodes(numaNodeID)
			availableCPUsForDevice := allocatableCPUs.Difference(assignedCPUs).Intersection(numaCPUs)
			logger.V(4).Info("NUMA node CPU availability", "numaNodeID", numaNodeID, "numaCPUs", numaCPUs.String(), "availableCPUs", availableCPUsForDevice.String())
			cur, err = cp.cpuAllocator.Allocate(logger, availableCPUsForDevice, preferredCPUs, claimCPUCount)
		case device.GROUP_BY_MACHINE:
			opaqueCPUSet, ok, err := cpuallocator.GetOpaqueCPUSet(logger, cp.driverName, claim.Status.Allocation, alloc)
			if err != nil {
				return kubeletplugin.PrepareResult{Err: err}
			}
			if !ok {
				return kubeletplugin.PrepareResult{Err: fmt.Errorf("no opaque cpuset configuration found for allocation request %q", alloc.Request)}
			}

			if err := cpuallocator.ValidateOpaqueCPUSet(cp.cpuAllocationStore.GetPreparedCPUs(), opaqueCPUSet, assignedCPUs, claimCPUCount, cp.topology.onlineCPUs, cp.topology.reservedCPUs); err != nil {
				return kubeletplugin.PrepareResult{Err: err}
			}
			cur = opaqueCPUSet
			logger.V(2).Info("using opaque config CPU assignment", "device", alloc.Device, "assigned", cur.String())
		}

		if err != nil {
			return kubeletplugin.PrepareResult{Err: err}
		}
		if err := cp.cpuAllocator.Validate(cur, assignedCPUs, cp.cpuAllocationStore.GetPreparedCPUs()); err != nil {
			return kubeletplugin.PrepareResult{Err: err}
		}
		assignedCPUs = assignedCPUs.Union(cur)
		logger.V(2).Info("CPU assignment for device", "device", alloc.Device, "assigned", cur.String(), "allAssigned", assignedCPUs.String())
	}

	if assignedCPUs.Size() == 0 {
		logger.V(6).Info("claim has no CPU allocations for this driver")
		return kubeletplugin.PrepareResult{}
	}

	// Reserve before CDI I/O so concurrent Prepare calls cannot select the same CPUs.
	if err := cp.reserveResourceClaimAllocation(logger, claim.UID, assignedCPUs); err != nil {
		return kubeletplugin.PrepareResult{Err: err}
	}
	result := cp.prepareDevices(logger, claim, assignedCPUs)
	if result.Err != nil {
		cp.cpuAllocationStore.RemoveResourceClaimAllocation(logger, claim.UID)
		return result
	}
	cp.metricsRecorder().RecordClaimAllocatedCPUs(assignedCPUs.Size())
	cp.refreshAllocationMetrics()
	return result
}

func (cp *CPUDriver) prepareResourceClaim(logger logr.Logger, claim *resourceapi.ResourceClaim) kubeletplugin.PrepareResult {
	logger.V(4).Info("preparing individual resource claim")

	if claim.Status.Allocation == nil {
		return kubeletplugin.PrepareResult{
			Err: fmt.Errorf("claim %s/%s has no allocation", claim.Namespace, claim.Name),
		}
	}

	claimCPUIDs := []int{}
	for _, alloc := range claim.Status.Allocation.Devices.Results {
		if alloc.Driver != cp.driverName {
			continue
		}
		cpuID, ok := cp.topology.deviceNameToCPUID[alloc.Device]
		if !ok {
			return kubeletplugin.PrepareResult{
				Err: fmt.Errorf("device %q not found in device to CPU ID map", alloc.Device),
			}
		}
		claimCPUIDs = append(claimCPUIDs, cpuID)
	}

	if len(claimCPUIDs) == 0 {
		logger.V(6).Info("claim has no CPU allocations for this driver")
		return kubeletplugin.PrepareResult{}
	}

	claimCPUSet := cpuset.New(claimCPUIDs...)
	if existingCPUs, ok := cp.cpuAllocationStore.GetResourceClaimAllocation(claim.UID); ok {
		logger.V(2).Info("claim already has allocated CPUs in store, reusing assignment", "cpus", existingCPUs.String())
		if !existingCPUs.Equals(claimCPUSet) {
			// This should realistically never happen as the claim is immutable.
			return kubeletplugin.PrepareResult{
				Err: fmt.Errorf("claim %s/%s is already prepared with different CPUs %s (requested %s)", claim.Namespace, claim.Name, existingCPUs.String(), claimCPUSet.String()),
			}
		}
		return cp.prepareDevices(logger, claim, existingCPUs)
	}

	// All the CPUs allocated to a claim must not be prepared for another claim.
	allocatableCPUs := cp.cpuAllocationStore.GetSharedCPUs()
	if !claimCPUSet.IsSubsetOf(allocatableCPUs) {
		return kubeletplugin.PrepareResult{
			Err: fmt.Errorf("claim %s/%s has overlapping device assignment with other claims", claim.Namespace, claim.Name),
		}
	}

	// Reserve before CDI I/O so concurrent Prepare calls cannot select the same CPUs.
	if err := cp.reserveResourceClaimAllocation(logger, claim.UID, claimCPUSet); err != nil {
		return kubeletplugin.PrepareResult{Err: err}
	}
	result := cp.prepareDevices(logger, claim, claimCPUSet)
	if result.Err != nil {
		cp.cpuAllocationStore.RemoveResourceClaimAllocation(logger, claim.UID)
		return result
	}
	cp.metricsRecorder().RecordClaimAllocatedCPUs(claimCPUSet.Size())
	cp.refreshAllocationMetrics()
	return result
}

func (cp *CPUDriver) prepareDevices(logger logr.Logger, claim *resourceapi.ResourceClaim, claimCPUSet cpuset.CPUSet) kubeletplugin.PrepareResult {
	deviceName := getCDIDeviceName(claim.UID)
	envVar := fmt.Sprintf("%s_%s=%s", cdiEnvVarPrefix, claim.UID, claimCPUSet.String())
	if err := cp.cdiMgr.AddDevice(logger, deviceName, envVar); err != nil {
		return kubeletplugin.PrepareResult{Err: err}
	}

	qualifiedName := cdiparser.QualifiedName(cdiVendor, cdiClass, deviceName)
	logger.V(6).Info("prepared CDI device", "cdiDeviceName", deviceName, "envVar", envVar, "qualifiedName", qualifiedName)
	preparedDevices := []kubeletplugin.Device{}
	for _, allocResult := range claim.Status.Allocation.Devices.Results {
		if allocResult.Driver != cp.driverName {
			continue
		}
		preparedDevice := kubeletplugin.Device{
			PoolName:     allocResult.Pool,
			DeviceName:   allocResult.Device,
			CDIDeviceIDs: []string{qualifiedName},
		}
		if allocResult.Request != "" {
			preparedDevice.Requests = []string{allocResult.Request}
		}
		if attrs, ok := getDeviceAttributes(cp.topology.deviceSlices, allocResult.Device); ok && len(attrs) > 0 {
			metadataAttrs := make(map[string]resourceapi.DeviceAttribute, len(attrs))
			for k, v := range attrs {
				metadataAttrs[string(k)] = v
			}
			if quantity, ok := allocResult.ConsumedCapacity[device.CPUResourceQualifiedName]; ok {
				allocatedCount := quantity.Value()
				metadataAttrs[string(device.AttributeAllocatedNumCPUs)] = resourceapi.DeviceAttribute{
					IntValue: &allocatedCount,
				}
			}
			preparedDevice.Metadata = &kubeletplugin.DeviceMetadata{
				Attributes: metadataAttrs,
			}
			logger.V(6).Info("added device metadata", "device", allocResult.Device, "numAttrs", len(metadataAttrs))
		}
		preparedDevices = append(preparedDevices, preparedDevice)
	}

	logger.V(4).Info("prepared devices for resource claim", "preparedDevices", preparedDevices)
	return kubeletplugin.PrepareResult{
		Devices: preparedDevices,
	}
}

// UnprepareResourceClaims is called by the kubelet to unprepare the resources for a claim.
func (cp *CPUDriver) UnprepareResourceClaims(ctx context.Context, claims []kubeletplugin.NamespacedObject) (map[types.UID]error, error) {
	_, logger := ctxlog.WithValues(ctx, "opID", generateShortID(opIDLen))

	logger.V(4).Info("begin: unpreparing resource claims", "numClaims", len(claims))
	defer logger.V(4).Info("end: unpreparing resource claims", "numClaims", len(claims))

	result := make(map[types.UID]error)

	if len(claims) == 0 {
		return result, nil
	}

	for _, claim := range claims {
		// note kubeletplugin.NamespacedObject doesn't implement KMetadata
		cLogger := logger.WithValues("claim", claim.String(), "claimUID", claim.UID)
		cLogger.V(2).Info("unpreparing resource claim")
		err := cp.unprepareResourceClaim(cLogger, claim)
		result[claim.UID] = err
		if err != nil {
			cLogger.Error(err, "error unpreparing resources for claim")
			cp.metricsRecorder().RecordUnprepare(cpumetrics.ResultError)
		} else {
			cp.metricsRecorder().RecordUnprepare(cpumetrics.ResultSuccess)
			cp.refreshAllocationMetrics()
		}
	}
	return result, nil
}

func (cp *CPUDriver) metricsRecorder() cpumetrics.Recorder {
	if cp.metrics == nil {
		return cpumetrics.Noop()
	}
	return cp.metrics
}

func (cp *CPUDriver) refreshAllocationMetrics() {
	if cp.cpuAllocationStore == nil {
		return
	}
	snapshot := cp.cpuAllocationStore.Snapshot()
	cp.metricsRecorder().SetAllocationState(cpumetrics.AllocationState{
		AllocatedCPUs:        snapshot.AllocatedCPUs,
		AvailableCPUs:        snapshot.AvailableCPUs,
		ReservedCPUs:         snapshot.ReservedCPUs,
		ActiveResourceClaims: snapshot.ActiveResourceClaims,
	})
}

func (cp *CPUDriver) unprepareResourceClaim(logger logr.Logger, claim kubeletplugin.NamespacedObject) error {
	// Remove the CDI spec first. If that fails, keep the allocation recorded so
	// the driver does not make those CPUs available while stale CDI state remains.
	if err := cp.cdiMgr.RemoveDevice(logger, getCDIDeviceName(claim.UID)); err != nil {
		return err
	}
	cp.cpuAllocationStore.RemoveResourceClaimAllocation(logger, claim.UID)
	cp.claimTracker.Cleanup(claim.UID)
	// TODO(#279): Update existing shared containers here once all supported runtimes can
	// safely process unsolicited NRI UpdateContainers calls. Until then, each container
	// picks up the expanded cpuset on its next CreateContainer or Synchronize.
	return nil
}

// HandleError is called by the kubelet plugin framework when an error occurs in the background,
// for example while publishing ResourceSlices.
func (cp *CPUDriver) HandleError(ctx context.Context, err error, msg string) {
	logger := ctxlog.FromContext(ctx)

	// Log the error using the standard Kubernetes error handler
	runtime.HandleErrorWithContext(ctx, err, msg)

	// For unrecoverable errors, exit immediately with a clear error message.
	// This fail-fast behavior is intentional for early project maturity to surface
	// issues quickly rather than silently continuing in a broken state.
	if !errors.Is(err, kubeletplugin.ErrRecoverable) {
		logger.Error(err, "fatal unrecoverable error in DRA driver, exiting",
			"driver", cp.driverName,
			"node", cp.nodeName,
			"message", msg,
		)
		ctxlog.Flush()
		os.Exit(1)
	}
}
