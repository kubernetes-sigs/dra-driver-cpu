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

// Internal package test: has direct access to the unexported schema metadata.
package driverconfig

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestGenerateDriverConfigSchema_CoversAllFields: every Config json field is
// either excluded on purpose (schemaExcludedFields) or present in the
// generated schema's properties.
func TestGenerateDriverConfigSchema_CoversAllFields(t *testing.T) {
	out, err := GenerateDriverConfigSchema()
	if err != nil {
		t.Fatalf("GenerateDriverConfigSchema() error: %v", err)
	}

	var doc struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(out, &doc); err != nil {
		t.Fatalf("unmarshaling generated schema: %v", err)
	}

	typ := reflect.TypeFor[Config]()
	for field := range typ.Fields() {
		jsonName, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if jsonName == "" || jsonName == "-" {
			continue
		}
		_, present := doc.Properties[jsonName]
		if _, excluded := schemaExcludedFields[jsonName]; excluded {
			if present {
				t.Errorf("Config field %q (json key %q) is marked excluded but appears in the generated schema", field.Name, jsonName)
			}
			continue
		}
		if !present {
			t.Errorf("Config field %q (json key %q) is missing from the generated schema", field.Name, jsonName)
		}
	}
}

// TestGenerateDriverConfigSchema_ExcludesKubeletRootDir verifies the kubelet
// root is kept out of the generated schema. It has to match the hostPath mounts
// the chart renders from the same value, so the chart owns it and a user cannot
// set it in the config file, the same as bindAddress and exposePCIeRoots.
func TestGenerateDriverConfigSchema_ExcludesKubeletRootDir(t *testing.T) {
	out, err := GenerateDriverConfigSchema()
	if err != nil {
		t.Fatalf("GenerateDriverConfigSchema: %v", err)
	}

	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(out, &schema); err != nil {
		t.Fatalf("unmarshal generated schema: %v\n%s", err, out)
	}

	if _, ok := schema.Properties["kubeletRootDir"]; ok {
		t.Errorf("kubeletRootDir must not appear in the generated schema; got:\n%s", out)
	}
	if want := "use helm chart's kubeletRootDir instead"; schemaExcludedFields["kubeletRootDir"] != want {
		t.Errorf("kubeletRootDir exclusion hint = %q, want %q", schemaExcludedFields["kubeletRootDir"], want)
	}
}
