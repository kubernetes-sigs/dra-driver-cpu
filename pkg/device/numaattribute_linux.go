//go:build linux

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
	"k8s.io/dynamic-resource-allocation/deviceattribute"
)

// numaNodeAttributeValue returns the value of the standard "numaNode" device
// attribute for a device bound to a single NUMA node, in scalar form.
//
// The value is built by the upstream deviceattribute helpers so the attribute
// name, its type and the validation (a NUMA node must be non-negative) are
// owned by the DRA library instead of being re-implemented here.
func numaNodeAttributeValue(numaNodeID int) (resourceapi.DeviceAttribute, error) {
	attr, err := deviceattribute.GetNUMANodeAttribute(numaNodeID, deviceattribute.ScalarAttribute)
	if err != nil {
		return resourceapi.DeviceAttribute{}, err
	}
	return attr.Value, nil
}
