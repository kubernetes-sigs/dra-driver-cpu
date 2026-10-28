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

package cpupartition

import (
	"fmt"
	"path/filepath"
	"strings"
)

// CgroupPath returns an OCI cgroupsPath relative to the cgroup root. It takes
// a cgroupfs path or a systemd slice:prefix:name, and rejects anything that
// would need cleaning rather than cleaning it.
func CgroupPath(raw string) (string, error) {
	if strings.Contains(raw, ":") {
		parts := strings.Split(raw, ":")
		if len(parts) != 3 || !strings.HasSuffix(parts[0], ".slice") || parts[0] == "-.slice" {
			return "", fmt.Errorf("unsupported systemd cgroup path %q", raw)
		}
		for _, part := range parts {
			if part == "" || strings.ContainsAny(part, "/\\\x00") || part == "." || part == ".." {
				return "", fmt.Errorf("invalid systemd cgroup path %q", raw)
			}
		}
		// systemd nests a-b-c.slice as a.slice/a-b.slice/a-b-c.slice.
		names := strings.Split(strings.TrimSuffix(parts[0], ".slice"), "-")
		path := ""
		for i, name := range names {
			if name == "" {
				return "", fmt.Errorf("invalid slice %q", parts[0])
			}
			path = filepath.Join(path, strings.Join(names[:i+1], "-")+".slice")
		}
		raw = filepath.Join(path, parts[1]+"-"+parts[2]+".scope")
	}
	for _, part := range strings.Split(raw, "/") {
		if part == ".." || part == "." {
			return "", fmt.Errorf("invalid cgroup path %q", raw)
		}
	}
	path := strings.TrimPrefix(raw, "/")
	if path == "" || !filepath.IsLocal(path) || filepath.Clean(path) != path {
		return "", fmt.Errorf("invalid cgroup path %q", raw)
	}
	return path, nil
}

// chain lists path and its ancestors below the root, outermost first. Each of
// them must grant the CPUs of a partition at path.
func chain(path string) []string {
	parts := strings.Split(path, "/")
	dirs := make([]string, len(parts))
	for i := range parts {
		dirs[i] = strings.Join(parts[:i+1], "/")
	}
	return dirs
}

// within reports whether path is dir or below it.
func within(path, dir string) bool {
	return path == dir || strings.HasPrefix(path, dir+"/")
}
