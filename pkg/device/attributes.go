/*
Copyright The Kubernetes Authors.

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

package device

import (
	resourceapi "k8s.io/api/resource/v1"
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
	AttributeNUMANodeID resourceapi.QualifiedName = "dra.cpu/numaNodeID"
	AttributeSocketID   resourceapi.QualifiedName = "dra.cpu/socketID"
	AttributeSMTEnabled resourceapi.QualifiedName = "dra.cpu/smtEnabled"
	AttributeCacheL3ID  resourceapi.QualifiedName = "dra.cpu/cacheL3ID"
	AttributeCoreType   resourceapi.QualifiedName = "dra.cpu/coreType"
	AttributeCoreID     resourceapi.QualifiedName = "dra.cpu/coreID"
	AttributeCPUID      resourceapi.QualifiedName = "dra.cpu/cpuID"
	AttributeNumCPUs    resourceapi.QualifiedName = "dra.cpu/numCPUs"
	// AttributeAllocatedNumCPUs is a metadata-only attribute (not published in
	// ResourceSlice) that indicates how many CPUs were allocated to a specific
	// claim from a grouped device's capacity.
	AttributeAllocatedNumCPUs resourceapi.QualifiedName = "dra.cpu/allocatedNumCPUs"
)

// SetCompatibilityAttributes add attributes to enable compatibility (e.g. alignment) with other
// DRA resource drivers leveraging attributes which are not kubernetes standard.
// This is the "staging area" which enables attribute sharing until (or before) they become standard.
func SetCompatibilityAttributes(attrs map[resourceapi.QualifiedName]resourceapi.DeviceAttribute, numaID int64) {
	attrs["dra.net/numaNode"] = resourceapi.DeviceAttribute{IntValue: new(numaID)}
}
