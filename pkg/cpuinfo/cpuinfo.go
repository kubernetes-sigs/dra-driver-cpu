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

package cpuinfo

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/cpuset"
)

// CoreType is an enum for the type of CPU core.
type CoreType int

const (
	// CoreTypeUndefined is the default zero value.
	CoreTypeUndefined CoreType = iota
	// CoreTypeStandard is a standard CPU core.
	CoreTypeStandard
	// CoreTypePerformance is a performance core (p-core).
	CoreTypePerformance
	// CoreTypeEfficiency is an efficiency core (e-core).
	CoreTypeEfficiency
)

// String returns the string representation of a CoreType.
func (c CoreType) String() string {
	switch c {
	case CoreTypeStandard:
		return "standard"
	case CoreTypePerformance:
		return "p-core"
	case CoreTypeEfficiency:
		return "e-core"
	default:
		return ""
	}
}

// MarshalJSON implements the json.Marshaler interface.
func (c CoreType) MarshalJSON() ([]byte, error) {
	s := c.String()
	if s == "" {
		// Omit the field if the type is undefined.
		return nil, nil
	}
	return json.Marshal(s)
}

func (c *CoreType) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	key := strings.ToLower(s)
	switch key {
	case "standard":
		*c = CoreTypeStandard
	case "p-core":
		*c = CoreTypePerformance
	case "e-core":
		*c = CoreTypeEfficiency
	default:
		return fmt.Errorf("unknown core type: %q", key)
	}
	return nil
}

// CPUInfo holds information about a single CPU.
type CPUInfo struct {
	// CpuID is the enumerated CPU ID
	CpuID int `json:"cpuID"`

	// CoreID is the logical core ID, unique within each SocketID
	CoreID int `json:"coreID"`

	// SocketID is the physical socket ID
	SocketID int `json:"socketID"`

	// ClusterID is the cluster ID, which sits between Socket and Core on some architectures (e.g. ARM)
	ClusterID int `json:"clusterID"`

	// NUMANodeID is the NUMA node ID, unique within each SocketID
	NUMANodeID int `json:"numaNodeID"`

	// NUMANodeCPUSet represents the set of CPUs that are in the same NUMA node.
	NumaNodeCPUSet cpuset.CPUSet `json:"numaNodeCPUSet"`

	// CPU Sibling of the CpuID
	SiblingCPUID int `json:"sibling"`

	// Core Type (e-core or p-core)
	CoreType CoreType `json:"coreType,omitempty"`

	// UncoreCacheID is the L3 cache ID
	UncoreCacheID int `json:"uncoreCacheID"`
}

// CPUTopology contains details of node cpu, where :
// CPU  - logical CPU, cadvisor - thread
// Core - physical CPU, cadvisor - Core
// Socket - socket, cadvisor - Socket
// NUMA Node - NUMA cell, cadvisor - Node
// UncoreCache - Split L3 Cache Topology, cadvisor
type CPUTopology struct {
	NumCPUs        int
	NumCores       int
	NumUncoreCache int
	NumSockets     int
	NumNUMANodes   int
	SMTEnabled     bool
	CPUDetails     CPUDetails
}

// SystemCPUInfo provides information about the CPUs on the system.
type SystemCPUInfo struct{}

// NewSystemCPUInfo creates a new SystemCPUInfo.
func NewSystemCPUInfo() *SystemCPUInfo {
	return &SystemCPUInfo{}
}

// GetCPUTopology returns the CPUTopology of the system.
func (s *SystemCPUInfo) GetCPUTopology() (*CPUTopology, error) {
	cpuInfos, err := s.GetCPUInfos()
	if err != nil {
		return nil, fmt.Errorf("failed to get CPU infos: %w", err)
	}

	cpuDetails := make(CPUDetails)
	sockets := sets.NewInt()
	numaNodes := sets.NewInt()
	type coreIdent struct {
		SocketID  int
		ClusterID int
		CoreID    int
	}
	cores := sets.New[coreIdent]()
	uncoreCaches := sets.NewInt()

	for i := range cpuInfos {
		info := cpuInfos[i]
		cpuDetails[info.CpuID] = info
		sockets.Insert(info.SocketID)
		numaNodes.Insert(info.NUMANodeID)
		// A core is unique by socket, cluster, and core id
		coreKey := coreIdent{SocketID: info.SocketID, ClusterID: info.ClusterID, CoreID: info.CoreID}
		cores.Insert(coreKey)
		if info.UncoreCacheID != -1 {
			uncoreCaches.Insert(info.UncoreCacheID)
		}
	}

	smtEnabled, err := s.IsSMTEnabled()
	if err != nil {
		log.Printf("Warning: could not determine SMT status from sysfs: %v. Falling back to CPU/Core count.", err)
		smtEnabled = len(cpuInfos) > cores.Len()
	}

	return &CPUTopology{
		NumCPUs:        len(cpuInfos),
		NumCores:       cores.Len(),
		NumSockets:     sockets.Len(),
		NumNUMANodes:   numaNodes.Len(),
		NumUncoreCache: uncoreCaches.Len(),
		SMTEnabled:     smtEnabled,
		CPUDetails:     cpuDetails,
	}, nil
}

// IsSMTEnabled checks if SMT is enabled on the system by reading /sys/devices/system/cpu/smt/control.
func (s *SystemCPUInfo) IsSMTEnabled() (bool, error) {
	status, err := ReadFile(hostSys("devices/system/cpu/smt/control"))
	if err != nil {
		return false, err
	}

	status = strings.TrimSpace(strings.ToLower(status))
	if status == "on" {
		return true, nil
	}
	if status == "off" || status == "forceoff" || status == "notsupported" || status == "notimplemented" {
		return false, nil
	}
	return false, fmt.Errorf("unknown SMT status: %s", status)
}

// GetCPUInfos returns a slice of CPUInfo structs, one for each logical CPU.
func (s *SystemCPUInfo) GetCPUInfos() ([]CPUInfo, error) {
	// Discover online CPUs from sysfs
	onlineFilename := hostSys("devices/system/cpu/online")
	onlineContent, err := ReadFile(onlineFilename)
	if err != nil {
		return []CPUInfo{}, fmt.Errorf("could not read online CPUs from %s: %w", onlineFilename, err)
	}

	onlineCPUs, err := cpuset.Parse(strings.TrimSpace(onlineContent))
	if err != nil {
		return []CPUInfo{}, fmt.Errorf("could not parse online CPUs '%s': %w", onlineContent, err)
	}

	// Intel-specific hybrid detection (P-cores vs E-cores)
	isHybrid := false
	var eCoreCpus cpuset.CPUSet
	eCoreFilename := hostSys("devices/cpu_atom/cpus")
	if _, err := os.Stat(eCoreFilename); err == nil {
		eCoreLines, err := ReadLines(eCoreFilename)
		if err == nil {
			isHybrid = true
			eCoreCpus, err = cpuset.Parse(eCoreLines[0])
			if err != nil {
				return []CPUInfo{}, err
			}
		}
	}

	cpuInfos := make([]CPUInfo, 0, onlineCPUs.Size())
	for _, cpuID := range onlineCPUs.List() {
		cpuInfo := CPUInfo{
			CpuID:          cpuID,
			SocketID:       -1,
			CoreID:         -1,
			ClusterID:      -1,
			NUMANodeID:     -1,
			NumaNodeCPUSet: cpuset.New(),
			UncoreCacheID:  -1,
			SiblingCPUID:   -1,
			CoreType:       CoreTypeUndefined,
		}

		if isHybrid {
			if eCoreCpus.Contains(cpuID) {
				cpuInfo.CoreType = CoreTypeEfficiency
			} else {
				cpuInfo.CoreType = CoreTypePerformance
			}
		} else {
			cpuInfo.CoreType = CoreTypeStandard
		}

		if err := populateTopologyInfo(&cpuInfo); err != nil {
			log.Printf("Warning: skipping CPU %d due to incomplete topology: %v", cpuID, err)
			continue
		}

		cpuInfos = append(cpuInfos, cpuInfo)
	}

	populateCpuSiblings(cpuInfos)

	return cpuInfos, nil
}

func populateTopologyInfo(cpuInfo *CPUInfo) error {
	cpuID := cpuInfo.CpuID

	// Get Socket ID from sysfs
	socketPath := hostSys(fmt.Sprintf("devices/system/cpu/cpu%d/topology/physical_package_id", cpuID))
	socketStr, err := ReadFile(socketPath)
	if err != nil {
		return fmt.Errorf("could not read socket_id for cpu %d from sysfs: %w", cpuID, err)
	}
	socketID, err := strconv.Atoi(strings.TrimSpace(socketStr))
	if err != nil {
		return fmt.Errorf("could not parse socket_id %q for cpu %d: %w", socketStr, cpuID, err)
	}
	cpuInfo.SocketID = socketID

	// Get Cluster ID from sysfs. While this sysfs file is present on most
	// architectures, on ARM this defines the physical boundary for shared resources (like L2 cache).
	clusterPath := hostSys(fmt.Sprintf("devices/system/cpu/cpu%d/topology/cluster_id", cpuID))
	clusterStr, err := ReadFile(clusterPath)
	if err == nil {
		clusterID, err := strconv.Atoi(strings.TrimSpace(clusterStr))
		if err != nil {
			log.Printf("Warning: could not parse cluster_id %q for cpu %d: %v", clusterStr, cpuID, err)
			cpuInfo.ClusterID = -1
		} else {
			// The kernel default for an undefined cluster id is -1.
			// However, due to 16-bit unsigned type casting, sysfs exposes this as 65535.
			// https://www.kernel.org/doc/Documentation/admin-guide/cputopology.rst
			if clusterID == 65535 {
				cpuInfo.ClusterID = -1
			} else {
				cpuInfo.ClusterID = clusterID
			}
		}
	} else {
		cpuInfo.ClusterID = -1 // Default to -1 if not present
	}

	// Get Core ID from sysfs
	corePath := hostSys(fmt.Sprintf("devices/system/cpu/cpu%d/topology/core_id", cpuID))
	coreStr, err := ReadFile(corePath)
	if err != nil {
		return fmt.Errorf("could not read core_id for cpu %d from sysfs: %w", cpuID, err)
	}
	coreID, err := strconv.Atoi(strings.TrimSpace(coreStr))
	if err != nil {
		return fmt.Errorf("could not parse core_id %q for cpu %d: %w", coreStr, cpuID, err)
	}
	cpuInfo.CoreID = coreID

	// Get NUMA Node ID from sysfs
	nodePath := hostSys(fmt.Sprintf("devices/system/cpu/cpu%d", cpuID))
	files, err := os.ReadDir(nodePath)
	if err != nil {
		return fmt.Errorf("could not read NUMA node for cpu %d from sysfs: %w", cpuID, err)
	}

	foundNode := false
	for _, file := range files {
		if strings.HasPrefix(file.Name(), "node") {
			nodeID, err := strconv.Atoi(strings.TrimPrefix(file.Name(), "node"))
			if err != nil {
				log.Printf("Warning: could not parse NUMA node ID from %s: %v", file.Name(), err)
				continue
			}
			cpuInfo.NUMANodeID = nodeID
			foundNode = true

			// Get NUMA Affinity Mask
			cpuListPath := hostSys(fmt.Sprintf("devices/system/node/node%d/cpulist", nodeID))
			cpuListLines, err := ReadLines(cpuListPath)
			if err != nil || len(cpuListLines) == 0 {
				log.Printf("Warning: could not read NUMA affinity mask for node %d: %v", nodeID, err)
				continue
			}
			cpuInfo.NumaNodeCPUSet, err = cpuset.Parse(cpuListLines[0])
			if err != nil {
				log.Printf("Warning: could not parse NUMA affinity mask for node %d: %v", nodeID, err)
				continue
			}
			break
		}
	}
	if !foundNode {
		return fmt.Errorf("could not determine NUMA node for CPU %d", cpuID)
	}

	if cpuInfo.SocketID < 0 || cpuInfo.CoreID < 0 || cpuInfo.NUMANodeID < 0 {
		return fmt.Errorf("incomplete topology information for CPU %d (socket: %d, core: %d, NUMA node: %d)", cpuID, cpuInfo.SocketID, cpuInfo.CoreID, cpuInfo.NUMANodeID)
	}

	// Get L3 Cache ID
	cachePath := hostSys(fmt.Sprintf("devices/system/cpu/cpu%d/cache", cpuID))
	cacheEntries, err := os.ReadDir(cachePath)
	if err != nil {
		return fmt.Errorf("could not read cache dir %s: %w", cachePath, err)
	}

	for _, entry := range cacheEntries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "index") {
			continue
		}

		levelPath := filepath.Join(cachePath, entry.Name(), "level")
		levelStr, err := ReadFile(levelPath)
		if err != nil {
			continue
		}

		// We are only interested in L3 caches
		if strings.TrimSpace(levelStr) != "3" {
			continue
		}

		l3CacheDir := filepath.Join(cachePath, entry.Name())

		sharedCPUListPath := filepath.Join(l3CacheDir, "shared_cpu_list")
		sharedCPUListStr, err := ReadFile(sharedCPUListPath)
		if err != nil {
			return fmt.Errorf("could not read shared_cpu_list from %s: %w", sharedCPUListPath, err)
		}

		sharedCPUSet, err := cpuset.Parse(strings.TrimSpace(sharedCPUListStr))
		if err != nil {
			return fmt.Errorf("could not parse shared_cpu_list '%s': %w", sharedCPUListStr, err)
		}

		cacheIdPath := filepath.Join(l3CacheDir, "id")
		idStr, err := ReadFile(cacheIdPath)
		var id int
		if err != nil {
			// Fallback for ARM systems missing the 'id' file: use the lowest shared CPU ID as the cache ID.
			if sharedCPUSet.Size() > 0 {
				id = sharedCPUSet.List()[0]
			} else {
				id = cpuID
			}
		} else {
			id, err = strconv.Atoi(strings.TrimSpace(idStr))
			if err != nil {
				return fmt.Errorf("could not parse L3 cache id '%s': %w", idStr, err)
			}
		}

		cpuInfo.UncoreCacheID = id
		break
	}

	return nil
}

// TODO: Handle more complex sibling relationships (e.g. 4-way SMT) if needed in the future. For now we only handle 2-way hyperthreading which is the most common case.
func populateCpuSiblings(cpuInfos []CPUInfo) {
	// Define a key struct to identify a unique physical core.
	type coreLocation struct {
		socket  int
		cluster int
		core    int
	}

	// Map each physical core to the list of logical CPUs (siblings) on it.
	coreToCPU := make(map[coreLocation][]int)
	for _, info := range cpuInfos {
		key := coreLocation{socket: info.SocketID, cluster: info.ClusterID, core: info.CoreID}
		coreToCPU[key] = append(coreToCPU[key], info.CpuID)
	}

	// Create a map of CPU ID -> index for fast updates.
	cpuIndexMap := make(map[int]int, len(cpuInfos))
	for i, info := range cpuInfos {
		cpuIndexMap[info.CpuID] = i
	}

	// Iterate through the grouped CPUs and set the sibling IDs.
	for _, siblingIds := range coreToCPU {
		// handle 2-way hyper-threading.
		if len(siblingIds) == 2 {
			cpu1Id, cpu2Id := siblingIds[0], siblingIds[1]
			cpu1Index, cpu2Index := cpuIndexMap[cpu1Id], cpuIndexMap[cpu2Id]

			cpuInfos[cpu1Index].SiblingCPUID = cpu2Id
			cpuInfos[cpu2Index].SiblingCPUID = cpu1Id
		}
	}
}

// ReadFile reads contents from a file.
func ReadFile(filename string) (string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

// ReadLines reads contents from a file and splits them by new lines.
func ReadLines(filename string) ([]string, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")

	return lines, nil
}

func hostRoot(combineWith ...string) string {
	return GetEnv("HOST_ROOT", "/", combineWith...)
}

func hostSys(combineWith ...string) string {
	return hostRoot(combinePath("sys", combineWith...))
}

// GetEnv retrieves the environment variable key, or uses the default value.
func GetEnv(key string, otherwise string, combineWith ...string) string {
	value := os.Getenv(key)
	if value == "" {
		value = otherwise
	}

	return combinePath(value, combineWith...)
}

func combinePath(value string, combineWith ...string) string {
	switch len(combineWith) {
	case 0:
		return value
	case 1:
		return filepath.Join(value, combineWith[0])
	default:
		all := make([]string, len(combineWith)+1)
		all[0] = value
		copy(all[1:], combineWith)
		return filepath.Join(all...)
	}
}

// TODO: Refactor topology representation to handle asymmetric CPU distributions.
// The current funcs CPUsPerCore, CPUsPerSocket, CPUsPerUncore assume symmetry
// and would be inaccurate if CPUs are offlined asymmetrically or on heterogeneous systems.
// See https://github.com/kubernetes-sigs/dra-driver-cpu/pull/16#discussion_r2588301122
func (t *CPUTopology) CPUsPerCore() int {
	if t.NumCores == 0 {
		return 0
	}
	return t.NumCPUs / t.NumCores
}

func (t *CPUTopology) CPUsPerSocket() int {
	if t.NumSockets == 0 {
		return 0
	}
	return t.NumCPUs / t.NumSockets
}

func (t *CPUTopology) CPUsPerUncore() int {
	if t.NumUncoreCache == 0 {
		// Avoid division by zero. If there are no uncore caches, then
		// no CPUs can be in one.
		return 0
	}
	// Note: this is an approximation that assumes all uncore caches have the same number of CPUs.
	return t.NumCPUs / t.NumUncoreCache
}
