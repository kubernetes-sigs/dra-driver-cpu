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
	"errors"
	"fmt"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/net/multiplex"
	"github.com/containerd/nri/pkg/stub"
	"github.com/containerd/ttrpc"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	registerapi "k8s.io/kubelet/pkg/apis/pluginregistration/v1"
)

type fakeNRIPlugin struct {
	run  func(context.Context) error
	stop func()
}

func (p *fakeNRIPlugin) Run(ctx context.Context) error { return p.run(ctx) }
func (p *fakeNRIPlugin) Stop() {
	if p.stop != nil {
		p.stop()
	}
}

type stoppedKubeletPlugin struct {
	mockKubeletPlugin
	stops atomic.Int32
}

func (p *stoppedKubeletPlugin) Stop() { p.stops.Add(1) }

func TestStartPluginsWaitsForSynchronization(t *testing.T) {
	d, _ := newMetricsTestDriver(t)
	runEntered, synchronize := make(chan struct{}), make(chan struct{})
	syncDone := make(chan error, 1)
	var draCalls atomic.Int32
	dra := &stoppedKubeletPlugin{mockKubeletPlugin: mockKubeletPlugin{statusFunc: func(int32) *registerapi.RegistrationStatus {
		return &registerapi.RegistrationStatus{PluginRegistered: true}
	}}}
	started := make(chan error, 1)
	go func() {
		started <- d.startPlugins(t.Context(), func(a *nriAttempt) (nriPlugin, error) {
			return &fakeNRIPlugin{run: func(ctx context.Context) error {
				close(runEntered)
				select {
				case <-synchronize:
				case <-ctx.Done():
					return ctx.Err()
				}
				_, err := (&nriAttemptHandler{CPUDriver: d, attempt: a}).Synchronize(ctx, nil, nil)
				syncDone <- err
				if err != nil {
					return err
				}
				<-ctx.Done()
				return ctx.Err()
			}}, nil
		}, func(context.Context) (KubeletPlugin, error) {
			draCalls.Add(1)
			return dra, nil
		})
	}()
	<-runEntered
	require.Zero(t, draCalls.Load())
	close(synchronize)
	require.NoError(t, <-syncDone)
	require.NoError(t, <-started)
	require.Equal(t, int32(1), draCalls.Load())
	d.Stop()
	d.Stop()
	require.Equal(t, int32(1), dra.stops.Load())
}

func TestStartPluginsFailureDoesNotServeDRA(t *testing.T) {
	for _, scenario := range []string{"factory error", "sync failure", "cancel before sync", "DRA start failure"} {
		t.Run(scenario, func(t *testing.T) {
			d, _ := newMetricsTestDriver(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var draCalls, stops atomic.Int32
			wantErr := errors.New("test failure")
			err := d.startPlugins(ctx, func(a *nriAttempt) (nriPlugin, error) {
				if scenario == "factory error" {
					return nil, wantErr
				}
				return &fakeNRIPlugin{run: func(ctx context.Context) error {
					if scenario == "sync failure" {
						return wantErr
					}
					if scenario == "cancel before sync" {
						cancel()
						<-ctx.Done()
						return ctx.Err()
					}
					if _, err := (&nriAttemptHandler{CPUDriver: d, attempt: a}).Synchronize(ctx, nil, nil); err != nil {
						return err
					}
					<-ctx.Done()
					return ctx.Err()
				}, stop: func() { stops.Add(1) }}, nil
			}, func(context.Context) (KubeletPlugin, error) {
				draCalls.Add(1)
				return nil, wantErr
			})
			require.Error(t, err)
			// Includes cleanup before the health loop or DRA server exists.
			d.Stop()
			d.Stop()
			if scenario == "DRA start failure" {
				require.ErrorIs(t, err, wantErr)
				require.Equal(t, int32(1), draCalls.Load())
			} else {
				require.Zero(t, draCalls.Load())
			}
			if scenario != "factory error" {
				require.Positive(t, stops.Load())
			}
		})
	}
}

func TestNRIReconnectRejectsOldCallbacks(t *testing.T) {
	d, _ := newMetricsTestDriver(t)
	d.nri = &nriLifecycle{changed: make(chan struct{})}
	old := &nriAttempt{}
	d.nri.current = old
	handler := &nriAttemptHandler{CPUDriver: d, attempt: old}
	_, err := handler.Synchronize(t.Context(), nil, nil)
	require.NoError(t, err)
	claim := individualMetricsClaim("pending", "cpudev0")
	results, err := d.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.NoError(t, results[claim.UID].Err)
	d.nri.closeAttempt(old)
	next := &nriAttempt{}
	d.nri.current = next
	results, err = d.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{individualMetricsClaim("new", "cpudev1")})
	require.NoError(t, err)
	require.ErrorContains(t, results["new"].Err, "not synchronized")
	oldState := d.cpuAllocationStore
	_, err = handler.Synchronize(t.Context(), nil, nil)
	require.Error(t, err)
	require.Same(t, oldState, d.cpuAllocationStore)
	_, _, err = handler.CreateContainer(t.Context(), &api.PodSandbox{Uid: "pod"}, &api.Container{Id: "stale"})
	require.Error(t, err)
	_, err = handler.StopContainer(t.Context(), &api.PodSandbox{Uid: "pod"}, &api.Container{Id: "stale"})
	require.Error(t, err)
	err = handler.RemoveContainer(t.Context(), &api.PodSandbox{Uid: "pod"}, &api.Container{Id: "stale"})
	require.Error(t, err)
	// Cleanup remains safe while disconnected and must not be resurrected.
	removed, err := d.UnprepareResourceClaims(t.Context(), []kubeletplugin.NamespacedObject{{UID: claim.UID}})
	require.NoError(t, err)
	require.NoError(t, removed[claim.UID])
	_, err = (&nriAttemptHandler{CPUDriver: d, attempt: next}).Synchronize(t.Context(), nil, nil)
	require.NoError(t, err)
	d.nri.closeAttempt(old) // late close must not invalidate the new connection
	require.NoError(t, d.prepareReady(t.Context()))
	require.True(t, d.cpuAllocationStore.GetPreparedCPUs().IsEmpty())
}

func TestNRIDisconnectDuringSynchronization(t *testing.T) {
	d, _ := newMetricsTestDriver(t)
	// Use an unrelated driver-prepared claim from a cold runtime snapshot.
	cdi := &pausedRefreshCDI{mockCdiMgr: newMockCdiMgr(), entered: make(chan struct{}), release: make(chan struct{})}
	cdi.devices[getCDIDeviceName("a")] = "DRA_CPUSET_a=0"
	d.cdiMgr = cdi
	a := &nriAttempt{}
	d.nri = &nriLifecycle{current: a, changed: make(chan struct{})}
	old := d.cpuAllocationStore
	done := make(chan error, 1)
	go func() {
		_, err := (&nriAttemptHandler{CPUDriver: d, attempt: a}).Synchronize(t.Context(), []*api.PodSandbox{{Id: "sandbox", Uid: "pod"}}, []*api.Container{{Id: "app", Name: "app", PodSandboxId: "sandbox", Env: []string{"DRA_CPUSET_a=0"}}})
		done <- err
	}()
	<-cdi.entered
	d.nri.closeAttempt(a)
	close(cdi.release)
	require.ErrorContains(t, <-done, "closed during synchronization")
	require.Same(t, old, d.cpuAllocationStore)
	require.False(t, a.ready)
}

func TestNRIUnresponsiveAttemptIsNotRetried(t *testing.T) {
	d, _ := newMetricsTestDriver(t)
	d.nri = &nriLifecycle{changed: make(chan struct{}), startupTimeout: time.Millisecond, stopTimeout: time.Millisecond}
	release, runDone, stopDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	runner := &nriConnectionRunner{cp: d, newPlugin: func(*nriAttempt) (nriPlugin, error) {
		calls.Add(1)
		return &fakeNRIPlugin{run: func(context.Context) error { <-release; close(runDone); return nil }, stop: func() { <-release; close(stopDone) }}, nil
	}}
	err := runNRIPluginWithRetry(t.Context(), runner, maxAttempts)
	require.ErrorIs(t, err, errNRIUnresponsive)
	require.Equal(t, int32(1), calls.Load())
	require.Error(t, d.prepareReady(t.Context()))
	close(release)
	<-runDone
	<-stopDone
}

func TestNRIRetryUsesFreshConnection(t *testing.T) {
	d, _ := newMetricsTestDriver(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d.nri = &nriLifecycle{changed: make(chan struct{}), startupTimeout: time.Second, stopTimeout: time.Second}
	var previous *nriAttempt
	calls := 0
	runner := &nriConnectionRunner{cp: d, newPlugin: func(a *nriAttempt) (nriPlugin, error) {
		calls++
		if previous != nil {
			require.NotSame(t, previous, a)
			require.True(t, previous.closed)
		}
		previous = a
		return &fakeNRIPlugin{run: func(ctx context.Context) error {
			if calls == 1 {
				return fmt.Errorf("connection failed")
			}
			_, err := (&nriAttemptHandler{CPUDriver: d, attempt: a}).Synchronize(ctx, nil, nil)
			if err != nil {
				return err
			}
			cancel()
			return ctx.Err()
		}}, nil
	}}
	require.ErrorIs(t, runNRIPluginWithRetry(ctx, runner, maxAttempts), context.Canceled)
	require.Equal(t, 2, calls)
}

func TestStartPluginsTimeoutDoesNotRegister(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		d, _ := newMetricsTestDriver(t)
		var draCalls atomic.Int32
		started := time.Now()
		err := d.startPlugins(t.Context(), func(*nriAttempt) (nriPlugin, error) {
			return &fakeNRIPlugin{run: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }}, nil
		}, func(context.Context) (KubeletPlugin, error) {
			draCalls.Add(1)
			return &mockKubeletPlugin{}, nil
		})
		require.Error(t, err)
		require.True(t, errors.Is(err, context.DeadlineExceeded) || errors.Is(err, errNRIUnresponsive))
		d.Stop()
		require.Equal(t, nriStartupTimeout, time.Since(started))
		require.Zero(t, draCalls.Load())
	})
}

func TestRegistrationCancellationCleansUpPlugins(t *testing.T) {
	d, _ := newMetricsTestDriver(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	dra := &stoppedKubeletPlugin{mockKubeletPlugin: mockKubeletPlugin{statusFunc: func(int32) *registerapi.RegistrationStatus {
		cancel()
		return nil
	}}}
	err := d.startPlugins(ctx, func(a *nriAttempt) (nriPlugin, error) {
		return &fakeNRIPlugin{run: func(ctx context.Context) error {
			if _, err := (&nriAttemptHandler{CPUDriver: d, attempt: a}).Synchronize(ctx, nil, nil); err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		}}, nil
	}, func(context.Context) (KubeletPlugin, error) { return dra, nil })
	require.ErrorIs(t, err, context.Canceled)
	d.Stop()
	require.Equal(t, int32(1), dra.stops.Load())
}

type registrationRuntime struct {
	registered chan struct{}
}

func (r *registrationRuntime) RegisterPlugin(context.Context, *api.RegisterPluginRequest) (*api.Empty, error) {
	close(r.registered)
	return &api.Empty{}, nil
}
func (*registrationRuntime) UpdateContainers(context.Context, *api.UpdateContainersRequest) (*api.UpdateContainersResponse, error) {
	return nil, fmt.Errorf("unexpected unsolicited update")
}

// Exercise the real pinned stub over ttrpc: Configure is not sufficient to
// open the gate, and a multi-chunk Synchronize must finish before serving DRA.
func TestNRIStubReadinessProtocol(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	pluginConn, runtimeConn := net.Pipe()
	mux := multiplex.Multiplex(runtimeConn)
	t.Cleanup(func() { _ = mux.Close() })
	listener, err := mux.Listen(multiplex.RuntimeServiceConn)
	require.NoError(t, err)
	server, err := ttrpc.NewServer()
	require.NoError(t, err)
	t.Cleanup(func() { _ = server.Close() })
	runtime := &registrationRuntime{registered: make(chan struct{})}
	api.RegisterRuntimeService(server, runtime)
	go func() { _ = server.Serve(ctx, listener) }()
	conn, err := mux.Open(multiplex.PluginServiceConn)
	require.NoError(t, err)
	client := ttrpc.NewClient(conn)
	t.Cleanup(func() { _ = client.Close() })
	plugin := api.NewPluginClient(client)
	d, _ := newMetricsTestDriver(t)
	d.nri = &nriLifecycle{changed: make(chan struct{}), startupTimeout: time.Second, stopTimeout: time.Second}
	runner := &nriConnectionRunner{cp: d, newPlugin: func(a *nriAttempt) (nriPlugin, error) {
		return stub.New(&nriAttemptHandler{CPUDriver: d, attempt: a}, stub.WithPluginName(testDriverName), stub.WithPluginIdx("00"), stub.WithConnection(pluginConn), stub.WithOnClose(func() { d.nri.closeAttempt(a) }))
	}}
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	select {
	case <-runtime.registered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, err = plugin.Configure(ctx, &api.ConfigureRequest{RuntimeName: "test", RegistrationTimeout: 1000, RequestTimeout: 1000})
	require.NoError(t, err)
	require.Error(t, d.prepareReady(ctx))
	_, err = plugin.Synchronize(ctx, &api.SynchronizeRequest{More: true, Pods: []*api.PodSandbox{{Id: "sandbox", Uid: "pod"}}})
	require.NoError(t, err)
	require.Error(t, d.prepareReady(ctx))
	_, err = plugin.Synchronize(ctx, &api.SynchronizeRequest{})
	require.NoError(t, err)
	require.NoError(t, d.prepareReady(ctx))
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("NRI runner did not stop")
	}
	require.Error(t, d.prepareReady(t.Context()))
}

func TestStartPluginsDisconnectBeforeDRARegistration(t *testing.T) {
	d, _ := newMetricsTestDriver(t)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	defer d.Stop()
	firstAttempt := make(chan *nriAttempt, 1)
	reconnect := make(chan struct{})
	resynchronized := make(chan error, 1)
	attempts := 0
	dra := &stoppedKubeletPlugin{mockKubeletPlugin: mockKubeletPlugin{statusFunc: func(int32) *registerapi.RegistrationStatus {
		return &registerapi.RegistrationStatus{PluginRegistered: true}
	}}}
	err := d.startPlugins(ctx, func(a *nriAttempt) (nriPlugin, error) {
		attempts++
		first := attempts == 1
		if first {
			firstAttempt <- a
		}
		return &fakeNRIPlugin{run: func(ctx context.Context) error {
			if !first {
				select {
				case <-reconnect:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			_, err := (&nriAttemptHandler{CPUDriver: d, attempt: a}).Synchronize(ctx, nil, nil)
			if !first {
				resynchronized <- err
			}
			if err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		}}, nil
	}, func(context.Context) (KubeletPlugin, error) {
		// waitReady has returned, but DRA has not registered yet.
		d.nri.closeAttempt(<-firstAttempt)
		return dra, nil
	})
	require.NoError(t, err)
	claim := individualMetricsClaim("a", "cpudev0")
	prepared, err := d.PrepareResourceClaims(ctx, []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.ErrorContains(t, prepared[claim.UID].Err, "not synchronized")
	// A rejected Prepare must not leave a usable allocation behind.
	pod := &api.PodSandbox{Id: "sandbox", Uid: "pod"}
	container := &api.Container{Id: "app", Name: "app", PodSandboxId: pod.Id, Env: []string{"DRA_CPUSET_a=0"}}
	close(reconnect)
	select {
	case err = <-resynchronized:
		require.NoError(t, err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	_, _, err = d.CreateContainer(ctx, pod, container)
	require.Error(t, err)
	prepared, err = d.PrepareResourceClaims(ctx, []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.NoError(t, prepared[claim.UID].Err)
	adjustment, _, err := d.CreateContainer(ctx, pod, container)
	require.NoError(t, err)
	require.Equal(t, "0", adjustment.Linux.Resources.Cpu.Cpus)
	d.Stop()
	require.Equal(t, int32(1), dra.stops.Load())
}

func TestStartPluginsCancellationAtKubeletStart(t *testing.T) {
	// Keep Unix socket paths below the platform limit, including on macOS.
	dir, err := os.MkdirTemp("", "dra-")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, os.RemoveAll(dir)) })
	d, _ := newMetricsTestDriver(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	defer d.Stop()
	err = d.startPlugins(ctx, func(a *nriAttempt) (nriPlugin, error) {
		return &fakeNRIPlugin{run: func(ctx context.Context) error {
			_, err := (&nriAttemptHandler{CPUDriver: d, attempt: a}).Synchronize(ctx, nil, nil)
			if err != nil {
				return err
			}
			<-ctx.Done()
			return ctx.Err()
		}}, nil
	}, func(ctx context.Context) (KubeletPlugin, error) {
		cancel()
		return kubeletplugin.Start(ctx, d,
			kubeletplugin.DriverName("dra"),
			kubeletplugin.KubeClient(fake.NewClientset()),
			kubeletplugin.HealthService(false),
			kubeletplugin.RegistrarDirectoryPath(dir),
			kubeletplugin.PluginDataDirectoryPath(dir),
		)
	})
	require.ErrorIs(t, err, context.Canceled)
	d.Stop()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	require.Empty(t, entries, "cancellation must not leave DRA sockets behind")
}

func TestHandleErrorIgnoresStoppedGRPCServer(t *testing.T) {
	d, _ := newMetricsTestDriver(t)
	d.HandleError(t.Context(), grpc.ErrServerStopped, "DRA gRPC server failed")
}
