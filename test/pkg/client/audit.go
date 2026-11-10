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

package client

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type createLabelAudit struct {
	base  http.RoundTripper
	key   string
	value string
}

func (a *createLabelAudit) RoundTrip(req *http.Request) (*http.Response, error) {
	// if it's not a POST, it cannot be a k8s client create API
	if req.Method != http.MethodPost || !isObjectCreatePath(req.URL.Path) {
		return a.base.RoundTrip(req)
	}

	body, err := inspectRequestBody(req)
	if err != nil {
		return nil, fmt.Errorf("audit create request for %q: %w", req.URL.Path, err)
	}
	if len(body) == 0 {
		return a.base.RoundTrip(req)
	}

	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		// The typed clientset used by the e2e suite sends JSON,
		// so this only affects unexpected callers.
		return a.base.RoundTrip(req)
	}
	metadata, ok := obj["metadata"].(map[string]any)
	if !ok {
		return a.base.RoundTrip(req)
	}
	labels, _ := metadata["labels"].(map[string]any)
	if got, ok := labels[a.key]; ok && got == a.value {
		return a.base.RoundTrip(req)
	}

	kind, _ := obj["kind"].(string)
	if kind == "" {
		kind = "object"
	}
	return nil, fmt.Errorf("refusing to create %s %q: missing test ownership label %s=%s; add this resource type to the labeling clientset in test/pkg/client/labels.go",
		kind, req.URL.Path, a.key, a.value)
}

func inspectRequestBody(req *http.Request) ([]byte, error) {
	// the body is a one-shot stream, so we must either
	// use GetBody to be able to read without consume,
	// or to add back a copy.
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			return nil, err
		}
		defer body.Close()
		return io.ReadAll(body)
	}
	if req.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func isObjectCreatePath(path string) bool {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	for idx, part := range parts {
		switch part {
		case "api":
			rest := parts[idx+1:]
			if len(rest) == 2 {
				return true
			}
			if len(rest) == 4 && rest[1] == "namespaces" {
				return !isNamespaceSubresource(rest[3])
			}
		case "apis":
			rest := parts[idx+1:]
			if len(rest) == 3 {
				return true
			}
			if len(rest) == 5 && rest[2] == "namespaces" {
				return !isNamespaceSubresource(rest[4])
			}
		}
	}
	return false
}

func isNamespaceSubresource(name string) bool {
	return name == "finalize" || name == "status"
}
