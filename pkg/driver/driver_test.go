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
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	registerapi "k8s.io/kubelet/pkg/apis/pluginregistration/v1"
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

// TestWaitForRegistration covers an unexported function, which we would normally
// reach through its caller instead. CPUDriver.Start does a lot and takes no
// injectable dependencies, so there is no seam to reach this behaviour from
// outside the package today. Testing it directly is the exception rather than the
// pattern, and it can move behind Start once Start is separable.
func TestWaitForRegistration(t *testing.T) {
	const registrarPath = "/var/lib/kubelet/plugins_registry"
	rejection := func(reason string) *registerapi.RegistrationStatus {
		return &registerapi.RegistrationStatus{Error: reason}
	}

	for _, tc := range []struct {
		name string
		// status answers one poll. Calling cancel ends the wait the way a shutdown does.
		status       func(cancel context.CancelFunc, call int32) *registerapi.RegistrationStatus
		wantErr      bool
		wantErrIs    error
		wantInErr    []string
		wantNotInErr []string
		minCalls     int32
	}{
		{
			name: "registered on the first poll",
			status: func(context.CancelFunc, int32) *registerapi.RegistrationStatus {
				return &registerapi.RegistrationStatus{PluginRegistered: true}
			},
		},
		{
			name: "the newest rejection is the one reported",
			status: func(_ context.CancelFunc, call int32) *registerapi.RegistrationStatus {
				if call == 1 {
					return rejection("unsupported plugin API version")
				}
				return rejection("driver name already registered")
			},
			wantErr:      true,
			wantInErr:    []string{"driver name already registered"},
			wantNotInErr: []string{"unsupported plugin API version"},
			minCalls:     2,
		},
		{
			name: "unregistered with no reason given",
			status: func(context.CancelFunc, int32) *registerapi.RegistrationStatus {
				return &registerapi.RegistrationStatus{}
			},
			wantErr:   true,
			wantInErr: []string{"reported no reason"},
		},
		{
			name: "kubelet never reported a status",
			status: func(context.CancelFunc, int32) *registerapi.RegistrationStatus {
				return nil
			},
			wantErr:   true,
			wantInErr: []string{registrarPath},
		},
		{
			name: "a shutdown is not diagnosed",
			status: func(cancel context.CancelFunc, _ int32) *registerapi.RegistrationStatus {
				cancel()
				return nil
			},
			wantErr:      true,
			wantErrIs:    context.Canceled,
			wantNotInErr: []string{registrarPath},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			registrar := &mockKubeletPlugin{
				statusFunc: func(call int32) *registerapi.RegistrationStatus {
					return tc.status(cancel, call)
				},
			}

			err := waitForRegistration(ctx, registrar, registrarPath, time.Millisecond, 200*time.Millisecond)
			if tc.minCalls > 0 {
				require.GreaterOrEqual(t, registrar.statusCalls.Load(), tc.minCalls, "too few polls for this case to prove anything")
			}
			if !tc.wantErr {
				require.NoError(t, err)
				require.Equal(t, int32(1), registrar.statusCalls.Load(), "a registered plugin should end the wait right away")
				return
			}
			require.Error(t, err)
			if tc.wantErrIs != nil {
				require.ErrorIs(t, err, tc.wantErrIs)
			}
			for _, want := range tc.wantInErr {
				require.ErrorContains(t, err, want)
			}
			for _, unwanted := range tc.wantNotInErr {
				require.NotContains(t, err.Error(), unwanted)
			}
		})
	}
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
			require.Equal(t, tc.wantRegistrar, registrarDir(cp.kubeletRootDir))
			require.Equal(t, tc.wantPlugins, filepath.Join(cp.kubeletRootDir, "plugins"))
			require.Equal(t, tc.wantData, pluginDataDir(cp.kubeletRootDir, cp.driverName))
			// The socket dir must not be the shared plugins parent, per the
			// kubeletplugin "not shared" contract.
			require.NotEqual(t, filepath.Join(cp.kubeletRootDir, "plugins"), pluginDataDir(cp.kubeletRootDir, cp.driverName))
		})
	}
}

// The suffix the helper appends is fixed, so the arithmetic is what decides
// whether a root is usable.
func TestSocketPathFits(t *testing.T) {
	const driver = "dra.cpu"
	// The registrar path is the root plus "/plugins_registry/dra.cpu-reg.sock".
	suffix := len(filepath.Join("/", "plugins_registry", driver+"-reg.sock"))

	for _, tc := range []struct {
		name    string
		root    string
		wantErr bool
	}{
		{"default root", "/var/lib/kubelet", false},
		{"relocated root", "/mnt/fast/k8s/kubelet", false},
		{"exactly at the limit", "/" + strings.Repeat("x", unixPathMax-suffix-1), false},
		{"one byte over", "/" + strings.Repeat("x", unixPathMax-suffix), true},
		// Bytes, not characters: this is well under any character count that
		// would fit, and still too long for sun_path.
		{"multibyte, short in characters", "/" + strings.Repeat("界", 30), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkSocketPathFits(tc.root, driver)
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			require.Contains(t, err.Error(), "Unix socket path")
		})
	}
}

// Pins the check to Start, not only to itself. It runs before Start touches the
// filesystem or the kubelet, so it needs no more than the fields the check reads.
func TestStartRefusesARootWithNoRoomForTheSocket(t *testing.T) {
	cp := &CPUDriver{
		kubeletRootDir: "/" + strings.Repeat("x", unixPathMax),
		driverName:     "dra.cpu",
	}

	_, err := cp.Start(context.Background())

	require.Error(t, err)
	require.Contains(t, err.Error(), "Unix socket path")
}
