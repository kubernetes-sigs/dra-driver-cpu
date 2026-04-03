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
	"sync/atomic"
	"testing"

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
