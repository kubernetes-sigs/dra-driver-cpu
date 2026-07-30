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
	"fmt"
	"os"

	"github.com/go-logr/logr"
	"sigs.k8s.io/yaml"
)

type FileSource struct {
	confPath string
}

func FromFile(confPath string) FileSource {
	return FileSource{
		confPath: confPath,
	}
}

func (fs FileSource) Name() string {
	return fs.confPath
}

func (fs FileSource) Apply(logger logr.Logger, cfg *Config) error {
	if fs.confPath == "" {
		return nil // nothing to do
	}
	overrides, err := buildConfMap(fs.confPath)
	logger.V(6).Info("overrides", "stage", fs.Name(), "values", overrides)
	if err != nil {
		return err
	}
	return applyMap(cfg, overrides)
}

// buildConfMap loads the config file at filePath, validates and strips
// "apiVersion", and returns the resulting map.
func buildConfMap(filePath string) (map[string]any, error) {
	confMap, err := loadFile(filePath)
	if err != nil {
		return nil, err
	}

	if err := validateAPIVersion(confMap); err != nil {
		return nil, err
	}
	delete(confMap, "apiVersion")

	if err := rejectExcludedFields(confMap); err != nil {
		return nil, err
	}

	return confMap, nil
}

// rejectExcludedFields errors out if confMap sets any key in
// schemaExcludedFields; those fields aren't configurable via the config
// file regardless of how it's supplied (Helm's driverConfig or a raw
// --config file).
func rejectExcludedFields(confMap map[string]any) error {
	for jsonKey, alternative := range schemaExcludedFields {
		if _, ok := confMap[jsonKey]; ok {
			return fmt.Errorf("field %q is not configurable via the config file; %s", jsonKey, alternative)
		}
	}
	return nil
}

// loadFile reads and parses the YAML file at path into a map.
func loadFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	confMap := map[string]any{}
	if err := yaml.Unmarshal(data, &confMap); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	return confMap, nil
}

// validateAPIVersion checks confMap["apiVersion"] when present.
func validateAPIVersion(confMap map[string]any) error {
	raw, ok := confMap["apiVersion"]
	if !ok {
		return nil
	}
	apiVer, _ := raw.(string)
	if apiVer != ConfigAPIVersion {
		return fmt.Errorf("unsupported apiVersion %q, want %q", apiVer, ConfigAPIVersion)
	}
	return nil
}
