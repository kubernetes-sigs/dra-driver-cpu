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

package driverconfig

import (
	"flag"
	"fmt"
	"path/filepath"

	"github.com/go-logr/logr"
)

// flagToJSONKey maps CLI flag names to their Config JSON keys.
// Add an entry here whenever a new field is added to Config and AddFlags.
var flagToJSONKey = map[string]string{
	"kubeconfig":        "kubeconfig",
	"hostname-override": "hostnameOverride",
	"bind-address":      "bindAddress",
	"reserved-cpus":     "reservedCPUs",
	"cpu-device-mode":   "cpuDeviceMode",
	"group-by":          "groupBy",
	"expose-pcie-roots": "exposePCIeRoots",
	"sysfs-overlay":     "sysfsOverlay",
	"kubelet-root-dir":  "kubeletRootDir",
}

// Load merges the config file at filePath into base, giving CLI flags that were
// explicitly set (reported by fs.Visit) priority over file values, then
// normalizes and validates the result. When filePath is empty no file is read,
// but base is still normalized and validated. fs must already be parsed.
func Load(base Config, filePath string, fs *flag.FlagSet, logger logr.Logger) (Config, error) {
	logger.V(6).Info("config: after flags", base.LogValues()...)

	result := base

	if filePath != "" {
		// Collect the Config-backed flags that were set explicitly on the command
		// line so the file does not override them. Flags that are not in
		// flagToJSONKey (for example --config or the klog --v flag) are not Config
		// fields, so the file cannot override them and they are simply ignored
		// here. TestFlagToJSONKey_CoversAllFlags statically guarantees every
		// AddFlags flag is mapped, so an unmapped Config flag is caught in CI
		// rather than needing a runtime warning.
		explicitJSONKeys := map[string]bool{}
		fs.Visit(func(f *flag.Flag) {
			if jsonKey, ok := flagToJSONKey[f.Name]; ok {
				explicitJSONKeys[jsonKey] = true
			}
		})

		confMap, err := buildConfMap(filePath)
		if err != nil {
			return Config{}, fmt.Errorf("config file %q: %w", filePath, err)
		}

		// CLI-explicit flags win; drop their keys so the file doesn't override them.
		for jsonKey := range explicitJSONKeys {
			delete(confMap, jsonKey)
		}

		if err := applyMap(&result, confMap); err != nil {
			return Config{}, fmt.Errorf("applying config file %q: %w", filePath, err)
		}

		logger.V(6).Info("config: after file", result.LogValues()...)
	}

	// Normalize the kubelet root so the logged and gathered config reports the
	// value the driver and chart actually use. An empty value becomes the
	// default; a non-canonical absolute path (with "." or ".." or repeated
	// separators) is cleaned, matching filepath.Join on the driver side and
	// clean on the chart side.
	if result.KubeletRootDir == "" {
		result.KubeletRootDir = Default().KubeletRootDir
	} else {
		result.KubeletRootDir = filepath.Clean(result.KubeletRootDir)
	}

	// Always validate, so a bad value from a CLI flag with no config file is
	// rejected too, not only a bad value from the file. Name the file when one
	// contributed, so the operator knows where to look.
	if err := result.Validate(); err != nil {
		if filePath != "" {
			return Config{}, fmt.Errorf("validating configuration from %q: %w", filePath, err)
		}
		return Config{}, fmt.Errorf("validating configuration: %w", err)
	}

	return result, nil
}
