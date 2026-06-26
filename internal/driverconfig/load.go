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
}

// Load merges the config file at filePath into base, giving CLI flags that were
// explicitly set (reported by fs.Visit) priority over file values.
// If filePath is empty, base is returned unchanged. fs must already be parsed.
func Load(base Config, filePath string, fs *flag.FlagSet) (Config, error) {
	if filePath == "" {
		return base, nil
	}

	explicitJSONKeys := map[string]bool{}
	fs.Visit(func(f *flag.Flag) {
		if jsonKey, ok := flagToJSONKey[f.Name]; ok {
			explicitJSONKeys[jsonKey] = true
		}
	})

	confMap := map[string]interface{}{}
	if err := buildConfMap(confMap, filePath); err != nil {
		return Config{}, fmt.Errorf("config file %q: %w", filePath, err)
	}

	// CLI-explicit flags win; drop their keys so the file doesn't override them.
	for jsonKey := range explicitJSONKeys {
		delete(confMap, jsonKey)
	}

	result := base
	if err := applyMap(&result, confMap); err != nil {
		return Config{}, fmt.Errorf("applying config file %q: %w", filePath, err)
	}

	if err := result.Validate(); err != nil {
		return Config{}, fmt.Errorf("config file %q: %w", filePath, err)
	}

	return result, nil
}
