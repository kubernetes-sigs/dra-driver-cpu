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
	"fmt"

	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
)

// addNUMANodeAttribute publishes the standard "numaNode" device attribute in
// scalar form, using the upstream helper for its validation and value shape.
func addNUMANodeAttribute(attrs map[resourceapi.QualifiedName]resourceapi.DeviceAttribute, numaNodeID int) error {
	attr, err := deviceattribute.GetNUMANodeAttribute(numaNodeID, deviceattribute.ScalarAttribute)
	if err != nil {
		return fmt.Errorf("cannot publish the numaNode attribute for NUMA node %d: %w", numaNodeID, err)
	}
	attrs[attr.Name] = attr.Value
	return nil
}
