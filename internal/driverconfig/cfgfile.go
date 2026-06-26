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
	"encoding/json"
	"fmt"
	"os"

	"sigs.k8s.io/yaml"
)

// buildConfMap loads the config file at filePath into confMap.
// It validates and strips "apiVersion" before returning.
func buildConfMap(confMap map[string]interface{}, filePath string) error {
	if err := loadAndMerge(confMap, filePath); err != nil {
		return err
	}

	if err := validateAPIVersion(confMap); err != nil {
		return err
	}
	delete(confMap, "apiVersion")

	return nil
}

// loadAndMerge reads the YAML file at path and merges it into dst.
func loadAndMerge(dst map[string]interface{}, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}

	src := map[string]interface{}{}
	if err := yaml.Unmarshal(data, &src); err != nil {
		return fmt.Errorf("parsing %s: %w", path, err)
	}

	mergeMap(dst, src)
	return nil
}

// validateAPIVersion checks confMap["apiVersion"] when present.
func validateAPIVersion(confMap map[string]interface{}) error {
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

func mergeMap(dst, src map[string]interface{}) {
	for k, v := range src {
		dst[k] = v
	}
}

// applyMap applies only the keys present in m to cfg; absent keys are
// untouched (encoding/json.Unmarshal semantics).
func applyMap(cfg *Config, m map[string]interface{}) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("marshaling config map: %w", err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("applying config map: %w", err)
	}
	return nil
}
