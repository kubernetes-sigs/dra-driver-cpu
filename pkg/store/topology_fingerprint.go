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

package store

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"maps"
	"slices"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
)

const (
	preambleSize        = 16
	header       string = "tpfp"
	ver1tag      string = "v001" // always 4 bytes, always starting with "v". 999 possible versions should be enough.
)

type cpuFingerprintInfo struct {
	CPUID         int
	CoreID        int
	SocketID      int
	ClusterID     int
	NUMANodeID    int
	UncoreCacheID int
}

func (cpu cpuFingerprintInfo) ToBytes() []byte {
	fields := []int64{
		int64(cpu.CPUID),
		int64(cpu.CoreID),
		int64(cpu.SocketID),
		int64(cpu.ClusterID),
		int64(cpu.NUMANodeID),
		int64(cpu.UncoreCacheID),
	}
	record := make([]byte, 0, binary.Size(fields))
	for _, value := range fields {
		// just a convention. We started on x86, so little endian it is.
		record = binary.LittleEndian.AppendUint64(record, uint64(value)) // #nosec G115 -- Preserve two's-complement encoding of negative topology IDs.
	}
	return record
}

func MakeTopologyFingerprint(ct cpuinfo.CPUTopology) string {
	hs := sha256.New()
	// preamble, right-filled with zero bytes if needed
	var preamble [preambleSize]byte
	copy(preamble[:], header+ver1tag)
	for _, key := range slices.Sorted(maps.Keys(ct.CPUDetails)) {
		info := ct.CPUDetails[key]
		cpu := cpuFingerprintInfo{
			CPUID:         key,
			CoreID:        info.CoreID,
			SocketID:      info.SocketID,
			ClusterID:     info.ClusterID,
			NUMANodeID:    info.NUMANodeID,
			UncoreCacheID: info.UncoreCacheID,
		}
		hs.Write(cpu.ToBytes())
	}
	return string(preamble[:]) + hex.EncodeToString(hs.Sum(nil))
}
