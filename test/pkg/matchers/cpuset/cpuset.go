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

package cpuset

import (
	"fmt"

	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/discovery"
	"github.com/onsi/gomega/gcustom"
	"github.com/onsi/gomega/types"
	"k8s.io/utils/cpuset"
)

// Equal succeeds if actual.Equals(expected) is true.
func Equal(expected cpuset.CPUSet) types.GomegaMatcher {
	return gcustom.MakeMatcher(func(actual cpuset.CPUSet) (bool, error) {
		return actual.Equals(expected), nil
	}).WithTemplate("Expected CPUSet\n\t{{.FormattedActual}}\nto equal\n\t{{format .Data}}", expected)
}

// BeSubsetOf succeeds if actual.IsSubsetOf(superset) is true.
func BeSubsetOf(superset cpuset.CPUSet) types.GomegaMatcher {
	return gcustom.MakeMatcher(func(actual cpuset.CPUSet) (bool, error) {
		return actual.IsSubsetOf(superset), nil
	}).WithTemplate("Expected CPUSet\n\t{{.FormattedActual}}\nto be a subset of\n\t{{format .Data}}", superset)
}

// HaveNoOverlapWith succeeds if actual.Intersection(other) is empty.
func HaveNoOverlapWith(other cpuset.CPUSet) types.GomegaMatcher {
	return gcustom.MakeMatcher(func(actual cpuset.CPUSet) (bool, error) {
		return actual.Intersection(other).IsEmpty(), nil
	}).WithTemplate("Expected CPUSet\n\t{{.FormattedActual}}\nto have no overlap with\n\t{{format .Data}}", other)
}

// HaveSize succeeds if actual.Size() equals the expected count.
func HaveSize(expected int) types.GomegaMatcher {
	return gcustom.MakeMatcher(func(actual cpuset.CPUSet) (bool, error) {
		return actual.Size() == expected, nil
	}).WithTemplate("Expected CPUSet\n\t{{.FormattedActual}}\nto have size {{.Data}} but got size {{.Actual.Size}}", expected)
}

func BeDistributedAcrossNUMANodes(numaInfo map[int]discovery.DRACPUNUMAInfo, expectedSpread int) types.GomegaMatcher {
	return gcustom.MakeMatcher(func(actual cpuset.CPUSet) (bool, error) {
		distribution := make(map[int]int)
		for node, info := range numaInfo {
			cpuCount := info.CPUs.Intersection(actual).Size()
			if cpuCount == 0 {
				continue
			}
			distribution[node] = cpuCount
		}
		if len(distribution) != expectedSpread {
			return false, fmt.Errorf("NUMA distribution does not match expected spread %d  across active nodes: %v", expectedSpread, distribution)
		}
		// TODO: also check the spread is even, not skewed
		return true, nil
	}).WithTemplate("Expected CPUSet\n\t{{.FormattedActual}}\nto be spread as evenly as possible across NUMA nodes {{format .Data}}", numaInfo)
}
