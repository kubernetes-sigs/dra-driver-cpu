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

// Internal package test: has direct access to the unexported flagToJSONKey map.
package driverconfig

import (
	"flag"
	"reflect"
	"testing"
)

// TestFlagToJSONKey_CoversAllFlags: every AddFlags flag has a flagToJSONKey entry.
func TestFlagToJSONKey_CoversAllFlags(t *testing.T) {
	cfg := Config{}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg.AddFlags(fs)

	fs.VisitAll(func(f *flag.Flag) {
		if _, ok := flagToJSONKey[f.Name]; !ok {
			t.Errorf("flag %q is registered via AddFlags but missing from flagToJSONKey", f.Name)
		}
	})
}

// TestFlagToJSONKey_NoStaleEntries: every flagToJSONKey entry maps to a real flag.
func TestFlagToJSONKey_NoStaleEntries(t *testing.T) {
	cfg := Config{}
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	cfg.AddFlags(fs)

	for flagName := range flagToJSONKey {
		if fs.Lookup(flagName) == nil {
			t.Errorf("flagToJSONKey has entry %q but AddFlags does not register this flag", flagName)
		}
	}
}

// TestBoolJSONKeys_CoversAllBoolFields: every bool field in Config has a boolJSONKeys entry.
func TestBoolJSONKeys_CoversAllBoolFields(t *testing.T) {
	rt := reflect.TypeOf(Config{})
	for i := 0; i < rt.NumField(); i++ {
		f := rt.Field(i)
		if f.Type.Kind() != reflect.Bool {
			continue
		}
		jsonTag := f.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			continue
		}
		key := jsonTag
		if idx := len(key); idx > 0 {
			if comma := indexOf(jsonTag, ','); comma >= 0 {
				key = jsonTag[:comma]
			}
		}
		if !boolJSONKeys[key] {
			t.Errorf("Config field %q (json:%q) is bool but missing from boolJSONKeys", f.Name, key)
		}
	}
}

// TestBoolJSONKeys_NoStaleEntries: every boolJSONKeys entry maps to a real bool field.
func TestBoolJSONKeys_NoStaleEntries(t *testing.T) {
	rt := reflect.TypeOf(Config{})
	for key := range boolJSONKeys {
		found := false
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			jsonTag := f.Tag.Get("json")
			tagKey := jsonTag
			if comma := indexOf(jsonTag, ','); comma >= 0 {
				tagKey = jsonTag[:comma]
			}
			if tagKey == key {
				if f.Type.Kind() != reflect.Bool {
					t.Errorf("boolJSONKeys has %q but Config field %q is %s, not bool", key, f.Name, f.Type.Kind())
				}
				found = true
				break
			}
		}
		if !found {
			t.Errorf("boolJSONKeys has %q but no Config field has this json tag", key)
		}
	}
}

func indexOf(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
