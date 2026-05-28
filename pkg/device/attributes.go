/*
Copyright 2026 The Kubernetes Authors.

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
	"k8s.io/utils/ptr"
)

// SetCompatibilityAttributes add attributes to enable compatibility (e.g. alignment) with other
// DRA resource drivers leveraging attributes which are not kubernetes standard.
// This is the "staging area" which enables attribute sharing until (or before) they become standard.
func SetCompatibilityAttributes(attrs map[resourceapi.QualifiedName]resourceapi.DeviceAttribute, numaID int64) {
	attrs["dra.net/numaNode"] = resourceapi.DeviceAttribute{IntValue: ptr.To(numaID)}
}
