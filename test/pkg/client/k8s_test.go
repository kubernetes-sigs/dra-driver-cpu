/*
Copyright 2026 The Kubernetes Authors.

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
	"errors"
	"testing"
)

func TestNewK8SConfigMissingKubeconfigReturnsSentinel(t *testing.T) {
	t.Setenv("KUBECONFIG", "")

	_, err := NewK8SConfig()
	if !errors.Is(err, ErrMissingKubeconfig) {
		t.Fatalf("expected ErrMissingKubeconfig, got %v", err)
	}
}

func TestNewK8SConfigInvalidPathIsNotMissingKubeconfig(t *testing.T) {
	t.Setenv("KUBECONFIG", "/definitely/not/here")

	_, err := NewK8SConfig()
	if err == nil {
		t.Fatal("expected an error for invalid kubeconfig path")
	}
	if errors.Is(err, ErrMissingKubeconfig) {
		t.Fatalf("expected invalid-path error, got ErrMissingKubeconfig: %v", err)
	}
}
