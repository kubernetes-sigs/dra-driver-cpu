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

package driver

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/require"
)

type mockNRIRunner struct {
	runFunc func(ctx context.Context) error
	calls   atomic.Int32
}

func (m *mockNRIRunner) Run(ctx context.Context) error {
	m.calls.Add(1)
	return m.runFunc(ctx)
}

func TestRunNRIPluginWithRetry_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	runner := &mockNRIRunner{
		runFunc: func(ctx context.Context) error {
			cancel()
			return context.Canceled
		},
	}

	err := runNRIPluginWithRetry(ctx, runner, maxAttempts)
	require.ErrorIs(t, err, context.Canceled, "should return context.Canceled when context is cancelled")
	require.Equal(t, int32(1), runner.calls.Load(), "Run should be called exactly once before context cancel")
}

func TestRunNRIPluginWithRetry_ContextCancelledAfterSeveralRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	var calls atomic.Int32
	runner := &mockNRIRunner{
		runFunc: func(ctx context.Context) error {
			n := calls.Add(1)
			if n >= 3 {
				cancel()
				return context.Canceled
			}
			return fmt.Errorf("transient error")
		},
	}

	err := runNRIPluginWithRetry(ctx, runner, maxAttempts)
	require.ErrorIs(t, err, context.Canceled, "should return context.Canceled when context is cancelled")
	require.Equal(t, int32(3), calls.Load(), "Run should be called 3 times before context cancel")
}

func TestRunNRIPluginWithRetry_ExhaustsAttempts(t *testing.T) {
	ctx := context.Background()

	runner := &mockNRIRunner{
		runFunc: func(ctx context.Context) error {
			return fmt.Errorf("persistent error")
		},
	}

	err := runNRIPluginWithRetry(ctx, runner, 3)
	require.Error(t, err, "should return error after exhausting attempts")
	require.Equal(t, int32(3), runner.calls.Load(), "Run should be called exactly maxAttempts times")
}

func TestRunNRIPluginWithRetry_SuccessfulRunNoRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner := &mockNRIRunner{
		runFunc: func(ctx context.Context) error {
			cancel()
			return nil
		},
	}

	err := runNRIPluginWithRetry(ctx, runner, maxAttempts)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int32(1), runner.calls.Load())
}

func TestGenerateShortID(t *testing.T) {
	testCases := []struct {
		name   string
		length int
	}{
		{name: "zero length", length: 0},
		{name: "single char", length: 1},
		{name: "opIDLen", length: opIDLen},
		{name: "large", length: 64},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			id := generateShortID(tc.length)
			require.Len(t, id, tc.length)
			if tc.length == 0 {
				return
			}
			require.True(t, isHex(id))
		})
	}
}

func TestGenerateShortIDUnique(t *testing.T) {
	a := generateShortID(opIDLen)
	b := generateShortID(opIDLen)
	require.NotEqual(t, a, b)
}

func isHex(s string) bool {
	s = strings.ToLower(s)
	for i := 0; i < len(s); i++ {
		b := s[i]
		if b-'0' < 10 || b-'a' < 6 {
			continue
		}
		return false
	}
	return true
}

func TestKubeletDirDerivation(t *testing.T) {
	const driverName = "cpu.dra.example.com"

	// The registrar, plugins, and per-driver socket directories are always
	// derived from the kubelet root, both at the default location and when the
	// root is relocated. filepath.Join also cleans a trailing slash.
	for _, tc := range []struct {
		name          string
		root          string
		wantRegistrar string
		wantPlugins   string
		wantData      string
	}{
		{
			name:          "default kubelet root",
			root:          "/var/lib/kubelet",
			wantRegistrar: "/var/lib/kubelet/plugins_registry",
			wantPlugins:   "/var/lib/kubelet/plugins",
			wantData:      "/var/lib/kubelet/plugins/cpu.dra.example.com",
		},
		{
			name:          "relocated kubelet root with trailing slash",
			root:          "/mnt/fast/k8s/kubelet/",
			wantRegistrar: "/mnt/fast/k8s/kubelet/plugins_registry",
			wantPlugins:   "/mnt/fast/k8s/kubelet/plugins",
			wantData:      "/mnt/fast/k8s/kubelet/plugins/cpu.dra.example.com",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp := &CPUDriver{driverName: driverName, kubeletRootDir: tc.root}
			require.Equal(t, tc.wantRegistrar, cp.registrarDir())
			require.Equal(t, tc.wantPlugins, cp.pluginsDir())
			require.Equal(t, tc.wantData, cp.pluginDataDir())
			// The socket dir must not be the shared plugins parent, per the
			// kubeletplugin "not shared" contract.
			require.NotEqual(t, cp.pluginsDir(), cp.pluginDataDir())
		})
	}
}

func TestNewRejectsRelativeKubeletRootDir(t *testing.T) {
	// A non-empty relative root is rejected before any sysfs access, so this
	// stays hermetic. An empty root is defaulted (covered by dra_hooks tests
	// that build a Config without KubeletRootDir).
	cfg := Config{DriverName: "cpu.dra.example.com", KubeletRootDir: "relative/kubelet"}
	_, err := New(logr.Discard(), Providers{}, &cfg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "must be absolute")
}
