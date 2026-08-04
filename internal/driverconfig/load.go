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
	"k8s.io/apimachinery/pkg/util/sets"
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

// deprecatedFlags is the set of standalone CLI flags being phased out in
// favour of the same-named driverConfig field (issue #245).
var deprecatedFlags = sets.New(
	"cpu-device-mode",
	"group-by",
	"reserved-cpus",
	"hostname-override",
	"sysfs-overlay",
)

// WarnDeprecatedFlags logs a warning for each deprecated flag explicitly set
// on the command line. Not called by Load. gatherinfo also calls Load, but
// on other processes' flags, where a warning wouldn't make sense.
func WarnDeprecatedFlags(fs *flag.FlagSet, logger logr.Logger) {
	fs.Visit(func(f *flag.Flag) {
		if !deprecatedFlags.Has(f.Name) {
			return
		}
		logger.Info("flag is deprecated and will be removed in a future release; prefer the equivalent driverConfig field instead",
			"flag", f.Name, "driverConfigField", flagToJSONKey[f.Name])
	})
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

		logger.V(6).Info("config: after applying config file", result.LogValues()...)
	}

	// An empty value is left for Validate to refuse.
	if result.KubeletRootDir != "" {
		result.KubeletRootDir = filepath.Clean(result.KubeletRootDir)
	}

	// Validated even with no config file, so a bad flag is refused too. The file
	// is named as an input rather than as the source, since what is validated is
	// the merged result.
	if err := result.Validate(); err != nil {
		if filePath != "" {
			return Config{}, fmt.Errorf("validating the configuration after loading %q: %w", filePath, err)
		}
		return Config{}, fmt.Errorf("validating configuration: %w", err)
	}

	return result, nil
}
