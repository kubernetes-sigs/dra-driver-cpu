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

package driver

import (
	resourceapi "k8s.io/api/resource/v1"
)

const (
	AttributeNUMANodeID resourceapi.QualifiedName = "dra.cpu/numaNodeID"
	AttributeSocketID   resourceapi.QualifiedName = "dra.cpu/socketID"
	AttributeSMTEnabled resourceapi.QualifiedName = "dra.cpu/smtEnabled"
	AttributeCacheL3ID  resourceapi.QualifiedName = "dra.cpu/cacheL3ID"
	AttributeCoreType   resourceapi.QualifiedName = "dra.cpu/coreType"
	AttributeCoreID     resourceapi.QualifiedName = "dra.cpu/coreID"
	AttributeCPUID      resourceapi.QualifiedName = "dra.cpu/cpuID"
	AttributeNumCPUs    resourceapi.QualifiedName = "dra.cpu/numCPUs"
	AttributePCIeRoots  resourceapi.QualifiedName = "dra.cpu/pcieRoots"
)
