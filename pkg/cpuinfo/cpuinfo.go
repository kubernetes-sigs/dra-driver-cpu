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
	switch s {
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

	// NumaNode is the NUMA node ID, unique within each SocketID
	NumaNode int `json:"numaNode"`

	// NUMA Node Affinity Mask
	NumaNodeAffinityMask string `json:"numaNodeAffinityMask"`

	// CPU Sibling of the CpuID
	SiblingCpuID int `json:"sibling"`

	// Core Type (e-core or p-core)
	CoreType CoreType `json:"coreType,omitempty"`

	// L3CacheID is the L3 cache ID
	L3CacheID int64 `json:"l3CacheID"`
}

// SystemCPUInfo provides information about the CPUs on the system.
type SystemCPUInfo struct{}

// NewSystemCPUInfo creates a new SystemCPUInfo.
func NewSystemCPUInfo() *SystemCPUInfo {
	return &SystemCPUInfo{}
}

// GetCPUInfos returns a slice of CPUInfo structs, one for each logical CPU.
func (s *SystemCPUInfo) GetCPUInfos() ([]CPUInfo, error) {
	filename := hostProc("cpuinfo")
	lines, err := ReadLines(filename)
	if err != nil {
		return []CPUInfo{}, err
	}

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

	cpuInfos := []CPUInfo{}
	var cpuInfoLines []string
	for _, line := range lines {
		// `/proc/cpuinfo` uses empty lines to denote a new CPU block of data.
		if strings.TrimSpace(line) == "" {
			// Parse and reset CPU lines.
			cpuInfo := parseCPUInfo(isHybrid, eCoreCpus, cpuInfoLines...)
			if cpuInfo != nil {
				cpuInfos = append(cpuInfos, *cpuInfo)
			}
			cpuInfoLines = []string{}
		} else {
			// Gather CPU info lines for later processing.
			cpuInfoLines = append(cpuInfoLines, line)
		}
	}
	if err := populateTopologyInfo(cpuInfos); err != nil {
		return nil, fmt.Errorf("failed to populate topology info: %w", err)
	}
	if err := populateL3CacheIDs(cpuInfos); err != nil {
		return nil, fmt.Errorf("failed to populate L3 cache IDs: %w", err)
	}
	populateCpuSiblings(cpuInfos)
	return cpuInfos, nil
}

func parseCPUInfo(isHybrid bool, eCoreCpus cpuset.CPUSet, lines ...string) *CPUInfo {
	cpuInfo := &CPUInfo{
		CpuID:                -1,
		SocketID:             -1,
		CoreID:               -1,
		NumaNode:             -1,
		NumaNodeAffinityMask: "",
		L3CacheID:            -1,
		SiblingCpuID:         -1,
		CoreType:             CoreTypeUndefined,
	}

	if len(lines) == 0 {
		return nil
	}

	for _, line := range lines {
		// Within each CPU block of data, each line uses ':' to separate the
		// key-value pair (with whitespace padding).
		fields := strings.Split(line, ":")
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSpace(fields[0])
		value := strings.TrimSpace(fields[1])

		switch key {
		case "processor":
			cpuInfo.CpuID = parseInt(value)
		case "physical id":
			cpuInfo.SocketID = parseInt(value)
		case "core id":
			cpuInfo.CoreID = parseInt(value)
		}
	}

	if isHybrid {
		if eCoreCpus.Contains(cpuInfo.CpuID) {
			cpuInfo.CoreType = CoreTypeEfficiency
		} else {
			cpuInfo.CoreType = CoreTypePerformance
		}
	} else {
		cpuInfo.CoreType = CoreTypeStandard
	}

	if cpuInfo.CpuID < 0 || cpuInfo.SocketID < 0 || cpuInfo.CoreID < 0 {
		return nil
	}

	return cpuInfo
}

func populateL3CacheIDs(cpuInfos []CPUInfo) error {
	for i := range cpuInfos {
		if cpuInfos[i].L3CacheID != -1 {
			continue
		}

		cachePath := hostSys(fmt.Sprintf("devices/system/cpu/cpu%d/cache", cpuInfos[i].CpuID))
		entries, err := os.ReadDir(cachePath)
		if err != nil {
			return fmt.Errorf("could not read cache dir %s: %w", cachePath, err)
		}

		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "index") {
				continue
			}

			levelPath := filepath.Join(cachePath, entry.Name(), "level")
			levelStr, err := ReadFile(levelPath)
			if err != nil {
				continue
			}

			if strings.TrimSpace(levelStr) == "3" {
				l3CacheDir := filepath.Join(cachePath, entry.Name())
				cacheIdPath := filepath.Join(l3CacheDir, "id")
				idStr, err := ReadFile(cacheIdPath)
				if err != nil {
					return fmt.Errorf("could not read L3 cache id from %s: %w", cacheIdPath, err)
				}
				id, err := strconv.ParseInt(strings.TrimSpace(idStr), 10, 64)
				if err != nil {
					return fmt.Errorf("could not parse L3 cache id '%s': %w", idStr, err)
				}

				sharedCPUListPath := filepath.Join(l3CacheDir, "shared_cpu_list")
				sharedCPUListStr, err := ReadFile(sharedCPUListPath)
				if err != nil {
					return fmt.Errorf("could not read shared_cpu_list from %s: %w", sharedCPUListPath, err)
				}

				sharedCPUSet, err := cpuset.Parse(strings.TrimSpace(sharedCPUListStr))
				if err != nil {
					return fmt.Errorf("could not parse shared_cpu_list '%s': %w", sharedCPUListStr, err)
				}

				// Update the L3Cache ID for all the cpus with the same cache.
				for j := range cpuInfos {
					if sharedCPUSet.Contains(cpuInfos[j].CpuID) {
						cpuInfos[j].L3CacheID = id
					}
				}
				break
			}
		}
	}
	return nil
}

func populateTopologyInfo(cpuInfos []CPUInfo) error {
	// Cache the affinity masks so we don't read the same file multiple times.
	numaMaskCache := make(map[int]string)

	for i := range cpuInfos {
		cpuID := cpuInfos[i].CpuID

		// Get Socket ID from sysfs (most reliable source)
		socketPath := hostSys(fmt.Sprintf("devices/system/cpu/cpu%d/topology/physical_package_id", cpuID))
		socketStr, err := ReadFile(socketPath)
		if err != nil {
			// If sysfs fails for some reason, we keep the value from /proc/cpuinfo
			log.Printf("Warning: could not read socket_id for cpu %d from sysfs: %v", cpuID, err)
		} else {
			// Overwrite with the definitive value from sysfs
			socketID, _ := strconv.Atoi(strings.TrimSpace(socketStr))
			cpuInfos[i].SocketID = socketID
		}

		// Get NUMA Node ID from sysfs
		nodePath := hostSys(fmt.Sprintf("devices/system/cpu/cpu%d", cpuID))
		files, err := os.ReadDir(nodePath)
		if err != nil {
			return fmt.Errorf("could not read cpu dir %s: %w", nodePath, err)
		}

		foundNode := false
		for _, file := range files {
			if strings.HasPrefix(file.Name(), "node") {
				nodeID, err := strconv.ParseInt(strings.TrimPrefix(file.Name(), "node"), 10, 64)
				if err != nil {
					continue // Should not happen with a well-formed sysfs
				}
				cpuInfos[i].NumaNode = int(nodeID)
				foundNode = true

				//  Get NUMA Affinity Mask (from cache if possible)
				mask, ok := numaMaskCache[int(nodeID)]
				if !ok {
					maskPath := hostSys(fmt.Sprintf("devices/system/node/node%d/cpumap", nodeID))
					maskLines, err := ReadLines(maskPath)
					if err != nil {
						return err
					}
					mask = formatAffinityMask(maskLines[0])
					numaMaskCache[int(nodeID)] = mask
				}
				cpuInfos[i].NumaNodeAffinityMask = mask
				break
			}
		}
		if !foundNode {
			log.Printf("Warning: could not determine NUMA node for CPU %d", cpuID)
		}
	}
	return nil
}

func populateCpuSiblings(cpuInfos []CPUInfo) {
	// Define a key struct to identify a unique physical core.
	type coreLocation struct {
		socket int
		core   int
	}

	// Map each physical core to the list of logical CPUs (siblings) on it.
	coreToCPU := make(map[coreLocation][]int)
	for _, info := range cpuInfos {
		key := coreLocation{socket: info.SocketID, core: info.CoreID}
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

			cpuInfos[cpu1Index].SiblingCpuID = cpu2Id
			cpuInfos[cpu2Index].SiblingCpuID = cpu1Id
		}
	}
}

func formatAffinityMask(mask string) string {
	if strings.TrimSpace(mask) == "" {
		return "0x0"
	}
	parts := strings.Split(mask, ",")
	// Reverse the parts to handle the kernel's little-endian word order.
	for i, j := 0, len(parts)-1; i < j; i, j = i+1, j-1 {
		parts[i], parts[j] = parts[j], parts[i]
	}
	newMask := strings.Join(parts, "")
	newMask = strings.TrimLeft(newMask, "0")
	if newMask == "" {
		return "0x0"
	}
	return "0x" + newMask
}

func parseInt(str string) int {
	val, err := strconv.Atoi(str)
	if err != nil {
		panic(err)
	}
	return val
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

func hostProc(combineWith ...string) string {
	return hostRoot(combinePath("proc", combineWith...))
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
