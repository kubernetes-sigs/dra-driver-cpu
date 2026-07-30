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
	"bytes"
	"encoding/json"
	"flag"
	"fmt"

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
}

var boolJSONKeys = map[string]bool{
	"exposePCIeRoots": true,
}

type Source interface {
	Name() string
	Apply(logger logr.Logger, cfg *Config) error
}

func Resolve(logger logr.Logger, sources []Source) (Config, error) {
	cfg := Default()
	logger.WithValues("stage", "default").V(6).Info("config", cfg.LogValues()...)

	for _, src := range sources {
		if err := src.Apply(logger, &cfg); err != nil {
			return Config{}, err
		}
		logger.WithValues("applied", src.Name()).V(6).Info("config", cfg.LogValues()...)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	logger.WithValues("stage", "validated").V(6).Info("config", cfg.LogValues()...)
	return cfg, nil
}

// Load merges the config file at filePath into base, giving CLI flags that were
// explicitly set (reported by fs.Visit) priority over file values.
// If filePath is empty, base is returned unchanged. fs must already be parsed.
func Load(base Config, filePath string, fs *flag.FlagSet, logger logr.Logger) (Config, error) {
	logger.V(6).Info("config: after flags", base.LogValues()...)

	if filePath == "" {
		return base, nil
	}

	explicitJSONKeys := map[string]bool{}
	fs.Visit(func(f *flag.Flag) {
		jsonKey, ok := flagToJSONKey[f.Name]
		if !ok {
			logger.Error(nil, "config: flag not found in flagToJSONKey map; its explicit CLI value may be silently overridden by the config file", "flag", f.Name)
			return
		}
		explicitJSONKeys[jsonKey] = true
	})

	confMap, err := buildConfMap(filePath)
	if err != nil {
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

	logger.V(6).Info("config: after file", result.LogValues()...)

	if err := result.Validate(); err != nil {
		return Config{}, fmt.Errorf("config file %q: %w", filePath, err)
	}

	return result, nil
}

// applyMap applies only the keys present in m to cfg; absent keys are
// untouched (encoding/json.Unmarshal semantics). Unknown keys are rejected
// to catch typos early rather than silently ignoring them.
func applyMap(cfg *Config, m map[string]any) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshaling config map: %w", err)
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(cfg); err != nil {
		return fmt.Errorf("applying config map: %w", err)
	}
	return nil
}
