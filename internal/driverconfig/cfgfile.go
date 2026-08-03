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
	"errors"
	"fmt"
	"maps"
	"os"
	"reflect"
	"slices"
	"strings"

	"sigs.k8s.io/yaml"
)

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

	// Before anything reads the map by name: the decoder folds case, the checks
	// below do not.
	if err := rejectNonCanonicalKeys(confMap); err != nil {
		return nil, err
	}

	if err := rejectExcludedFields(confMap); err != nil {
		return nil, err
	}

	return confMap, nil
}

// canonicalConfigKeys is the set of names a config file may use, taken from
// Config's own json tags so a field added later cannot be forgotten here. Direct
// fields and explicit tag names only: TestConfigDeclaresExplicitJSONNames rejects
// any other shape, so encoding/json's embedding and tag-dominance rules do not
// have to be reproduced here.
func canonicalConfigKeys() map[string]bool {
	// Not a Config field, so reflection cannot find it, but `ApiVersion` deserves
	// the same correction as any other misspelling.
	keys := map[string]bool{"apiVersion": true}
	for field := range reflect.TypeFor[Config]().Fields() {
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name != "" && name != "-" {
			keys[name] = true
		}
	}
	return keys
}

// rejectNonCanonicalKeys refuses a key that is not spelled the way Config
// declares it. encoding/json matches a field without regard to case while the
// checks around it compare exactly, so a folded spelling slips past both the
// excluded-field check and the explicit-flag precedence pass. Folding those
// comparisons instead would let two keys differing only in case decode into one
// field, so the file is refused rather than resolved. A name matching no field
// is left to DisallowUnknownFields.
func rejectNonCanonicalKeys(confMap map[string]any) error {
	canonical := slices.Sorted(maps.Keys(canonicalConfigKeys()))
	var problems []string
	// Sorted, and reporting every key it rejects: ranging a map gave one file a
	// different message on each node, and each fix cost another restart. The other
	// checks each stop at the first problem, since they run elsewhere.
	for _, key := range slices.Sorted(maps.Keys(confMap)) {
		if slices.Contains(canonical, key) {
			continue
		}
		for _, want := range canonical {
			if !strings.EqualFold(key, want) {
				continue
			}
			if alternative, excluded := schemaExcludedFields[want]; excluded {
				// Not the canonical spelling: the next check refuses that too, so
				// answer the question they are about to ask instead.
				problems = append(problems,
					fmt.Sprintf("field %q is not configurable via the config file; %s", key, alternative))
			} else {
				problems = append(problems,
					fmt.Sprintf("field %q is spelled differently from the schema; use %q", key, want))
			}
			break
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// rejectExcludedFields errors out if confMap sets any key in
// schemaExcludedFields; those fields aren't configurable via the config
// file regardless of how it's supplied (Helm's driverConfig or a raw
// --config file).
func rejectExcludedFields(confMap map[string]any) error {
	// Sorted, so a file setting two excluded fields names the same one every run.
	for _, jsonKey := range slices.Sorted(maps.Keys(schemaExcludedFields)) {
		if _, ok := confMap[jsonKey]; ok {
			return fmt.Errorf("field %q is not configurable via the config file; %s", jsonKey, schemaExcludedFields[jsonKey])
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

	// Strict for the duplicate keys, not the unknown ones: the target is a map, so
	// nothing is unknown, but Unmarshal keeps one of a repeated key at random.
	confMap := map[string]any{}
	if err := yaml.UnmarshalStrict(data, &confMap); err != nil {
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
