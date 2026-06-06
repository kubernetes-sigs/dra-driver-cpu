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

package buildinfo

import "runtime/debug"

type Info struct {
	GoVersion   string
	VCSRevision string
	VCSTime     string
}

func Read() Info {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return Info{}
	}

	version := Info{
		GoVersion: info.GoVersion,
	}

	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			version.VCSRevision = s.Value
		case "vcs.time":
			version.VCSTime = s.Value
		}
	}

	return version
}
