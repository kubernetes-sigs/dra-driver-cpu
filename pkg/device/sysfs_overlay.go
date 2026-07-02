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
	"fmt"
	"io/fs"
	"path"
	"strings"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/sysfs"
	"sigs.k8s.io/yaml"
)

// ParseSysFSOverlay parses a YAML object whose keys are absolute sysfs paths
// and whose values are the file contents to return for those paths.
func ParseSysFSOverlay(data []byte) (map[string]string, error) {
	rawOverlay := map[string]any{}
	if err := yaml.UnmarshalStrict(data, &rawOverlay); err != nil {
		return nil, fmt.Errorf("parse sysfs overlay: %w", err)
	}

	overlay := make(map[string]string, len(rawOverlay))
	for overlayPath, value := range rawOverlay {
		contents, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("parse sysfs overlay: value for %q must be a string", overlayPath)
		}
		overlay[overlayPath] = contents
	}
	return overlay, nil
}

// NewOverlaySysFS returns a read-through sysfs which serves exact matches from
// overlay and delegates all other operations to base. The overlay is copied
// and is therefore immutable after this function returns.
func NewOverlaySysFS(base sysfs.FS, overlay map[string]string) (sysfs.FS, error) {
	if base == nil {
		return nil, fmt.Errorf("base sysfs is nil")
	}
	if len(overlay) == 0 {
		return base, nil
	}

	files := make(map[string][]byte, len(overlay))
	for fullPath, contents := range overlay {
		name, err := sysFSName(fullPath)
		if err != nil {
			return nil, err
		}
		if _, ok := files[name]; ok {
			return nil, fmt.Errorf("duplicate sysfs overlay path %q", fullPath)
		}
		files[name] = []byte(contents)
	}

	for name := range files {
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			if _, ok := files[parent]; ok {
				return nil, fmt.Errorf("sysfs overlay path %q is both a file and a directory", path.Join(sysfs.Root, parent))
			}
		}
	}

	return newOverlayFS(base, files), nil
}

func sysFSName(fullPath string) (string, error) {
	if !strings.HasPrefix(fullPath, sysfs.Root+"/") {
		return "", fmt.Errorf("sysfs overlay path %q must be beneath %s", fullPath, sysfs.Root)
	}
	if clean := path.Clean(fullPath); clean != fullPath {
		return "", fmt.Errorf("sysfs overlay path %q is not clean", fullPath)
	}

	name := strings.TrimPrefix(fullPath, sysfs.Root+"/")
	if !fs.ValidPath(name) {
		return "", fmt.Errorf("invalid sysfs overlay path %q", fullPath)
	}
	return name, nil
}
