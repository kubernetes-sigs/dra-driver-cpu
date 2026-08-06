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

package main

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

const metadataBasePath = "/var/run/kubernetes.io/dra-device-attributes"

type metadataEntry struct {
	Path    string          `json:"path"`
	Content json.RawMessage `json:"content"`
}

func main() {
	var entries []metadataEntry

	err := filepath.WalkDir(metadataBasePath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".json" {
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // path is from WalkDir under a fixed base directory
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}
		entries = append(entries, metadataEntry{
			Path:    path,
			Content: json.RawMessage(data),
		})
		return nil
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error scanning metadata: %v\n", err)
		os.Exit(1)
	}

	if err := json.NewEncoder(os.Stdout).Encode(entries); err != nil {
		fmt.Fprintf(os.Stderr, "error encoding output: %v\n", err)
		os.Exit(2)
	}
}
