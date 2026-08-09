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

// Internal package test: uses reflection over the unexported Config fields.
package driverconfig

import (
	"reflect"
	"strings"
	"testing"
)

// TestConfigLogValues_CoversAllFields: every Config json field has a LogValues key.
func TestConfigLogValues_CoversAllFields(t *testing.T) {
	loggedKeys := map[string]bool{}
	values := Config{}.LogValues()
	for i := 0; i+1 < len(values); i += 2 {
		if key, ok := values[i].(string); ok {
			loggedKeys[key] = true
		}
	}

	typ := reflect.TypeFor[Config]()
	for field := range typ.Fields() {
		jsonName, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if jsonName == "" || jsonName == "-" {
			continue
		}
		if !loggedKeys[jsonName] {
			t.Errorf("Config field %q (json key %q) is missing from LogValues", field.Name, jsonName)
		}
	}
}

// TestConfigDump_IncludesZeroValues: every Config json field appears in Dump,
// even at its zero value.
func TestConfigDump_IncludesZeroValues(t *testing.T) {
	out := Config{}.Dump()

	typ := reflect.TypeFor[Config]()
	for field := range typ.Fields() {
		jsonName, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if jsonName == "" || jsonName == "-" {
			continue
		}
		if !strings.Contains(out, jsonName+":") {
			t.Errorf("Config field %q (json key %q) is missing from Dump output:\n%s", field.Name, jsonName, out)
		}
	}
}
