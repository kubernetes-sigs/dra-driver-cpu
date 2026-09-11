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
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"k8s.io/utils/cpuset"
)

const (
	AtomSep   = ";"
	StrideSep = ">"
	NoSibling = -1
)

// Compact SMT sibling representation aka `SMTMap`.
// We need to represent the CPU sibling representation and expose
// it in the resource slice, to be able to let consumers of the API
// to figure out which cpuid is sibling (hyperthread pair) with what.
// Encoding the simplest A:B (A is CPU sibling with B) relationship
// will very quickly run out of attribute space as the modern
// and upcoming CPU silicon easily exposes 512+ logical cores per socket.
// We therefore leverage the fact that CPU sibling relationship
// is not random but in almost all the existing (x86) silicon,
// the CPU sibling are linked by a fixed offset (we call it stride).
// For example (lscpu -e):
// CPU NODE SOCKET CORE L1d:L1i:L2:L3 ONLINE    MAXMHZ   MINMHZ       MHZ
//   0    0      0    0 0:0:0:0          yes 4800.0000 400.0000 1240.5400
//   1    0      0    1 1:1:1:0          yes 4800.0000 400.0000 1300.0000
//   2    0      0    2 2:2:2:0          yes 4800.0000 400.0000  818.8550
//   3    0      0    3 3:3:3:0          yes 4800.0000 400.0000  398.6430
//   4    0      0    0 0:0:0:0          yes 4800.0000 400.0000 1090.5090
//   5    0      0    1 1:1:1:0          yes 4800.0000 400.0000 1300.0000
//   6    0      0    2 2:2:2:0          yes 4800.0000 400.0000 1237.1780
//   7    0      0    3 3:3:3:0          yes 4800.0000 400.0000 1200.9640
// in this (laptop) excerpt CPUs 0,4 are logical cores from physical core 0;
// CPUs 1,5 are logical cores from the physical core 1 and so on.
// therefore, the stride is 4 and we can compactly represent the map with
// a notation intentionally reminiscent of cpusets:
// `A-B>S` `A` and `B` determine a cpuset, then `S` is the stride.
// So: `0-7>4` encodes the SMT map of the example machine.
// Meaning: for each CPU in the set, its sibling is at stride 4.
// Real modern silicon can have SMT and non-SMT core exposed in the same socket.
// We call the non-SMT cores `stranded` and, for now, we list them explicitly.
// An example could look like `0-7>4;8-11`. In this example, we have
// the same 0-7 CPUs with SMT enabled, and cores 8-11 without SMT in the same hardware.
// Likewise, the notation support mixed strides in the same machine, it
// will look like `0-7>4;8-11>2;12-15`.

// FormatSMTMap produces a compact string representation of the CPU
// siblings as learned from the given CPUTopology.
func FormatSMTMap(topo *cpuinfo.CPUTopology) string {
	strides := make(map[int][]int) // stride -> cpuIDs
	for _, info := range topo.CPUDetails {
		stride := cpuSMTStride(info)
		cpus, ok := strides[stride]
		if !ok {
			cpus = []int{}
		}
		if stride == 0 { // prevent NoSibling to leak into the cpuid slice
			strides[stride] = append(cpus, info.CpuID)
		} else {
			strides[stride] = append(cpus, info.CpuID, info.SiblingCPUID)
		}
	}
	// step 1: strided sets
	var sb strings.Builder
	for _, stride := range slices.Sorted(maps.Keys(strides)) {
		if stride == 0 {
			continue
		}
		cpuIDs := strides[stride]
		cpus := cpuset.New(cpuIDs...)
		sb.WriteString(AtomSep)
		sb.WriteString(cpus.String())
		sb.WriteString(StrideSep)
		sb.WriteString(strconv.Itoa(stride))
	}
	// step 2: non-strided (aka stranded) set
	if strandedCPUs, ok := strides[0]; ok {
		sb.WriteString(AtomSep)
		cpus := cpuset.New(strandedCPUs...)
		sb.WriteString(cpus.String())
	}
	// step 3: final validation and encoding
	smtMap := sb.String()
	if len(smtMap) == 0 {
		return ""
	}
	return smtMap[1:] // strip leading stray AtomSep
}

// DecodeSMTMap takes a serialized SMT map as produced by
// FormatSMTMap and returns a map {cpuid -> sibling_cpuid}.
// Some silicon may have mixed cores on which some have
// siblings, some not. In this case, sibling-less core have
// -1 as their sibling_cpuid.
// If the decoding fails, return a nil map and an error.
func DecodeSMTMap(data string) (map[int]int, error) {
	sibMap := make(map[int]int)
	for atom := range strings.SplitSeq(data, AtomSep) {
		// let's check what this atom is about
		pos := strings.Index(atom, StrideSep)
		// no StrideSep: it seems a stranded set
		if pos == -1 { // note this is the return value of `strings.Index`, not `NoSibling`
			stranded, err := cpuset.Parse(atom)
			if err != nil {
				return nil, fmt.Errorf("atom %q does not parse as stranded set: %w", atom, err)
			}
			for _, cpu := range stranded.UnsortedList() {
				sibMap[cpu] = NoSibling
			}
			continue
		}
		// min-atom is `A-B>S`
		//       index=<01234>
		// so finding the stride separator in a position earlier than
		// index 3 can only mean we are dealing with a malformed atom.
		if pos < 3 {
			return nil, fmt.Errorf("malformed atom: %q", atom)
		}
		// from now on it the atom can only be a strided set
		cpuRange, rawStride, ok := strings.Cut(atom, StrideSep)
		if !ok {
			return nil, fmt.Errorf("atom %q is malformed: missing stride separator", atom)
		}
		cpus, err := cpuset.Parse(cpuRange)
		if err != nil {
			return nil, fmt.Errorf("atom %q has a malformed cpuset: %w", atom, err)
		}
		stride, err := strconv.Atoi(rawStride)
		if err != nil {
			return nil, fmt.Errorf("atom %q has a malformed stride value: %w", atom, err)
		}
		if stride <= 0 {
			return nil, fmt.Errorf("atom %q has an invalid negative stride %d", atom, stride)
		}
		for _, cpuid := range cpus.List() { // sorting matters here
			if _, ok := sibMap[cpuid]; ok {
				// visiting a sibling added in a previous pass: nothing to do
				continue
			}
			sibling := cpuid + stride
			if !cpus.Contains(sibling) {
				return nil, fmt.Errorf("atom %q has CPU %d without sibling at stride %d", atom, cpuid, stride)
			}
			sibMap[cpuid] = sibling
			sibMap[sibling] = cpuid
		}
	}
	return sibMap, nil
}
