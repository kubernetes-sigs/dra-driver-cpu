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
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/driverconfig"
)

// TestRunUsesConfigSysFSOverlay: run() must build the sysfs overlay from cfg, not the pre-merge driverFlags.
func TestRunUsesConfigSysFSOverlay(t *testing.T) {
	cfg := driverconfig.Default()
	cfg.SysFSOverlay = filepath.Join(t.TempDir(), "does-not-exist.yaml")

	err := run(testr.New(t), cfg)
	if err == nil {
		t.Fatal("run() succeeded, want error reading missing sysfs overlay")
	}
	if !strings.Contains(err.Error(), "read sysfs overlay") {
		t.Fatalf("run() error = %v, want sysfs overlay read error (cfg.SysFSOverlay was ignored)", err)
	}
}

// TestRunFailsOnHTTPBindError: run() must report a bind failure before driver setup.
func TestRunFailsOnHTTPBindError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	cfg := driverconfig.Default()
	cfg.BindAddress = listener.Addr().String()

	err = run(testr.New(t), cfg)
	if err == nil {
		t.Fatal("run() succeeded, want HTTP bind error")
	}
	if want := "failed to bind HTTP server to " + cfg.BindAddress; !strings.Contains(err.Error(), want) {
		t.Fatalf("run() error = %v, want error containing %q", err, want)
	}
}

func TestResolveNodeName(t *testing.T) {
	host, _ := os.Hostname()
	tests := []struct {
		name     string
		override string
		env      string
		want     string
	}{
		{name: "override wins over env", override: "Override-Node", env: "env-node", want: "override-node"},
		{name: "env used when override empty", env: "Env-Node", want: "env-node"},
		{name: "falls back to os hostname", want: strings.ToLower(strings.TrimSpace(host))},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NODE_NAME", tc.env)
			got, err := resolveNodeName(tc.override)
			if err != nil {
				t.Fatalf("resolveNodeName() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("resolveNodeName() = %q, want %q", got, tc.want)
			}
		})
	}
}
