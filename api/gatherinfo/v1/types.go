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

// Package v1 defines the legacy gatherinfo report schema.
package v1

const (
	APIVersion = "v1"
	// LayoutVersion identifies the serialized report layout.
	LayoutVersion = APIVersion
)

// Report is a snapshot of the machine and driver configurations.
type Report struct {
	ToolVersion   ToolVersion  `json:"toolVersion"`
	LayoutVersion string       `json:"layoutVersion"`
	CPUDetails    CPUDetails   `json:"cpuDetails"`
	DriverConfig  DriverConfig `json:"driverConfig"`
}

// CPUDetails represents the machine CPU data
type CPUDetails struct {
	Topology TopologySummary `json:"topology"`
	CPUs     []CPU           `json:"cpus"`
}

// TopologySummary represents he machine CPU topology.
type TopologySummary struct {
	NumCPUs        int  `json:"numCPUs"`
	NumCores       int  `json:"numCores"`
	NumUncoreCache int  `json:"numUncoreCache"`
	NumSockets     int  `json:"numSockets"`
	NumNUMANodes   int  `json:"numNUMANodes"`
	SMTEnabled     bool `json:"smtEnabled"`
}

// CPU represents a machine logical CPU.
type CPU struct {
	CPUID          int    `json:"cpuID"`
	CoreID         int    `json:"coreID"`
	SocketID       int    `json:"socketID"`
	ClusterID      int    `json:"clusterID"`
	NUMANodeID     int    `json:"numaNodeID"`
	NUMANodeCPUSet string `json:"numaNodeCPUSet,omitempty"`
	Sibling        int    `json:"sibling"`
	CoreType       string `json:"coreType,omitempty"`
	UncoreCacheID  int    `json:"uncoreCacheID"`
}

// ToolVersion represents the golang toolchain used to build the driver.
type ToolVersion struct {
	GoVersion   string `json:"goVersion,omitempty"`
	VCSRevision string `json:"vcsRevision,omitempty"`
	VCSTime     string `json:"vcsTime,omitempty"`
}

// DriverConfig is a snapshot of the internal runtime data.
// Future version of `gatherinfo` will just expose the serialized most recent version driverconfig data.
// See the driverconfig API for details.
type DriverConfig struct {
	Kubeconfig                            string `json:"kubeconfig,omitempty"`
	HostnameOverride                      string `json:"hostnameOverride,omitempty"`
	BindAddress                           string `json:"bindAddress,omitempty"`
	ReservedCPUs                          string `json:"reservedCPUs,omitempty"`
	CPUDeviceMode                         string `json:"cpuDeviceMode,omitempty"`
	GroupBy                               string `json:"groupBy,omitempty"`
	ExposePCIeRoots                       bool   `json:"exposePCIeRoots,omitempty"`
	SysFSOverlay                          string `json:"sysfsOverlay,omitempty"`
	KubeletRootDir                        string `json:"kubeletRootDir,omitempty"`
	PublishNodeAllocatableResourceMapping bool   `json:"publishNodeAllocatableResourceMapping,omitempty"`
	Allocator                             string `json:"allocator,omitempty"`
}
