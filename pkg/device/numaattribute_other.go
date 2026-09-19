//go:build !linux

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

import resourceapi "k8s.io/api/resource/v1"

// numaNodeAttributeValue returns the value of the standard "numaNode" device
// attribute for a device bound to a single NUMA node, in scalar form.
//
// This is the non-linux counterpart of the function with the same name in
// numaattribute_linux.go. The upstream deviceattribute helpers are linux-only,
// and this driver only runs on linux, so this variant exists to keep the
// package building and its unit tests running on other platforms. It mirrors
// the scalar value the upstream helper produces.
func numaNodeAttributeValue(numaNodeID int) (resourceapi.DeviceAttribute, error) {
	return resourceapi.DeviceAttribute{IntValue: new(int64(numaNodeID))}, nil
}
