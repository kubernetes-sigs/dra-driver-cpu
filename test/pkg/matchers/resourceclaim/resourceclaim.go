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

package resourceclaim

import (
	"fmt"
	"slices"

	"github.com/onsi/gomega/gcustom"
	"github.com/onsi/gomega/types"
	resourcev1 "k8s.io/api/resource/v1"
)

type requestResultsExpectation struct {
	RequestName     string
	ExpectedResults int
}

// ReuseSameDeviceForRequest succeeds if the ResourceClaim has a non-nil
// allocation with exactly the expected number of results for the given request,
// and all those results point at the same allocated device.
func ReuseSameDeviceForRequest(requestName string, expectedResults int) types.GomegaMatcher {
	expectation := requestResultsExpectation{
		RequestName:     requestName,
		ExpectedResults: expectedResults,
	}
	return gcustom.MakeMatcher(func(actual *resourcev1.ResourceClaim) (bool, error) {
		if actual == nil {
			return false, fmt.Errorf("nil ResourceClaim")
		}
		if actual.Status.Allocation == nil {
			return false, nil
		}
		results := actual.Status.Allocation.Devices.Results // shortcut
		if len(results) != expectedResults {
			return false, nil
		}
		if len(results) == 0 {
			return false, nil
		}
		firstDevice := results[0].Device
		for _, result := range results {
			if result.Request != requestName || result.Device != firstDevice {
				return false, nil
			}
		}
		return true, nil
	}).WithTemplate("Expected ResourceClaim {{.Actual.Namespace}}/{{.Actual.Name}} to reuse the same device across {{.Data.ExpectedResults}} allocation results for request {{.Data.RequestName}}", expectation)
}

type requestResultsConsumption struct {
	ResourceName   string
	ExpectedAmount int
}

// HaveAllocationResultsAllConsuming succeeds if the ResourceClaim has a non-nil
// allocation with non-zero results, each consuming the given resource for the given among.
func HaveAllocationResultsAllConsuming(resourceName string, expectedAmount int) types.GomegaMatcher {
	expectation := requestResultsConsumption{
		ResourceName:   resourceName,
		ExpectedAmount: expectedAmount,
	}
	return gcustom.MakeMatcher(func(actual *resourcev1.ResourceClaim) (bool, error) {
		if actual == nil {
			return false, fmt.Errorf("nil ResourceClaim")
		}
		if actual.Status.Allocation == nil {
			return false, nil
		}
		results := actual.Status.Allocation.Devices.Results // shortcut
		if len(results) == 0 {
			return false, nil
		}
		for _, result := range results {
			capacity, ok := result.ConsumedCapacity[resourcev1.QualifiedName(resourceName)]
			if !ok {
				return false, nil
			}
			if capacity.CmpInt64(int64(expectedAmount)) != 0 {
				return false, nil
			}
		}
		return true, nil
	}).WithTemplate("Expected ResourceClaim {{.Actual.Namespace}}/{{.Actual.Name}} to have all allocation for results for resource {{.Data.ResourceName}} with value {{.Data.ExpectedAmount}}", expectation)
}

func HaveAllocationResultFor(names ...string) types.GomegaMatcher {
	return gcustom.MakeMatcher(func(actual *resourcev1.ResourceClaim) (bool, error) {
		if actual == nil {
			return false, fmt.Errorf("nil ResourceClaim")
		}
		if actual.Status.Allocation == nil {
			return false, nil
		}
		results := actual.Status.Allocation.Devices.Results // shortcut
		if len(results) != len(names) {
			return false, nil
		}
		gotNames := []string{}
		for _, result := range results {
			gotNames = append(gotNames, result.Request)
		}
		slices.Sort(names)
		slices.Sort(gotNames)
		ret := slices.Compare(names, gotNames)
		return ret == 0, nil
	}).WithTemplate("Expected ResourceClaim {{.Actual.Namespace}}/{{.Actual.Name}} to have all allocation results matching expected requests {{.Data}}", names)
}
