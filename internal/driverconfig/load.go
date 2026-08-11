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
// on the command line.
func WarnDeprecatedFlags(fs *flag.FlagSet, logger logr.Logger) {
	fs.Visit(func(f *flag.Flag) {
		if !deprecatedFlags.Has(f.Name) {
			return
		}
		logger.Info("flag is deprecated and will be removed in a future release; prefer the equivalent driverConfig field instead",
			"flag", f.Name, "driverConfigField", flagToJSONKey[f.Name])
	})
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
			return Config{}, fmt.Errorf("cannot apply %s: %w", src.Name(), err)
		}
		logger.WithValues("applied", src.Name()).V(6).Info("config", cfg.LogValues()...)
	}
	if cfg.KubeletRootDir != "" {
		cfg.KubeletRootDir = filepath.Clean(cfg.KubeletRootDir)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("cannot validate config: %w", err)
	}
	logger.WithValues("stage", "validated").V(6).Info("config", cfg.LogValues()...)
	return cfg, nil
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
