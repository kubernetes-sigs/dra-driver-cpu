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
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"
)

// runtimeSupportsUnsolicitedUpdate returns true if the runtime's NRI adaptation
// includes containerd/nri#301, which removed the lock that made unsolicited
// UpdateContainers calls deadlock. We infer it from the runtime version reported
// in the NRI Configure handshake: containerd 2.4.0+ and cri-o 1.37+ have it.
//
// Other runtimes (k3s, microk8s, vendor forks) fall back to false: we don't
// know which NRI they vendor, so the conservative choice is to stay off.
func runtimeSupportsUnsolicitedUpdate(name, version string) (bool, string) {
	// Normalize name to lowercase for case-insensitive match
	name = strings.ToLower(name)

	// Parse version, dropping build metadata and pre-release tags
	v, err := semver.NewVersion(version)
	if err != nil {
		return false, fmt.Sprintf("unknown runtime %s (invalid version %q)", name, version)
	}

	switch name {
	case "containerd":
		// containerd 2.4.0+ includes nri v0.12.3+ which has containerd/nri#301
		minVersion := semver.MustParse("2.4.0")
		if v.LessThan(minVersion) {
			return false, fmt.Sprintf("containerd %s predates nri v0.12.3 (needs 2.4.0+)", version)
		}
		return true, fmt.Sprintf("detected containerd %s", version)

	case "cri-o":
		// cri-o 1.37.0+ vendors nri v0.12.3+
		minVersion := semver.MustParse("1.37.0")
		if v.LessThan(minVersion) {
			return false, fmt.Sprintf("cri-o %s predates nri v0.12.3 (needs 1.37.0+)", version)
		}
		return true, fmt.Sprintf("detected cri-o %s", version)

	default:
		// k3s, microk8s, and other forks: we don't know which NRI they vendor.
		// The conservative choice is to default off.
		return false, fmt.Sprintf("unknown runtime %s/%s", name, version)
	}
}
