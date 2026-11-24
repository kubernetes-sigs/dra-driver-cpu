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

// Package runtimecompat decides which container runtimes are safe for NRI
// features that older runtime adaptations mishandle.
package runtimecompat

import (
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// Runtime names as reported in the NRI Configure handshake, and the first
// release of each that vendors an NRI with containerd/nri#301 (nri v0.12.1+):
// containerd 2.4.0 vendors nri v0.12.3, cri-o 1.37.0 vendors nri v0.12.2.
const (
	containerdName       = "containerd"
	containerdMinVersion = "2.4.0"
	crioName             = "cri-o"
	crioMinVersion       = "1.37.0"
)

// SupportsUnsolicitedUpdate reports whether the runtime's NRI adaptation
// includes containerd/nri#301, which removed the lock that made unsolicited
// UpdateContainers calls deadlock. The handshake reports the runtime version,
// not the NRI version it vendors, so this is an allowlist: other runtimes
// (k3s, microk8s, vendor forks) return false. The string explains the result.
func SupportsUnsolicitedUpdate(name, version string) (bool, string) {
	name = strings.ToLower(name)
	v, err := semver.NewVersion(version)
	if err != nil {
		return false, fmt.Sprintf("unknown runtime %s (invalid version %q)", name, version)
	}

	var minVersion string
	switch name {
	case containerdName:
		minVersion = containerdMinVersion
	case crioName:
		minVersion = crioMinVersion
	default:
		return false, fmt.Sprintf("unknown runtime %s/%s", name, version)
	}
	if v.LessThan(semver.MustParse(minVersion)) {
		return false, fmt.Sprintf("%s %s predates containerd/nri#301 (needs %s+)", name, version, minVersion)
	}
	return true, fmt.Sprintf("detected %s %s", name, version)
}
