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
	"strings"

	resourceapi "k8s.io/api/resource/v1"
)

// LookupByQualifiedName looks up key in m. The default domain
// qualifies keys which do not already have an explicit domain.
//
// The map may contain entries with or without a domain or (worse!) both for the same identifier.
// The entry with a fully-qualified name is preferred in case of such an ambiguity.
//
// Note: This function is copied from https://github.com/kubernetes/kubernetes/pull/142202.
// TODO: Replace with k8s.io/dynamic-resource-allocation/api.LookupByQualifiedName once dependencies are bumped to v0.38.
func LookupByQualifiedName[T any](m map[resourceapi.QualifiedName]T, key resourceapi.QualifiedName, defaultDomain string) (T, bool) {
	domain, name, hasDomain := strings.Cut(string(key), "/")
	if hasDomain {
		if v, ok := m[key]; ok {
			return v, true
		}
		if domain == defaultDomain {
			if v, ok := m[resourceapi.QualifiedName(name)]; ok {
				return v, true
			}
		}
	} else {
		if v, ok := m[resourceapi.QualifiedName(defaultDomain+"/"+string(key))]; ok {
			return v, true
		}
		if v, ok := m[key]; ok {
			return v, true
		}
	}

	var zero T
	return zero, false
}
