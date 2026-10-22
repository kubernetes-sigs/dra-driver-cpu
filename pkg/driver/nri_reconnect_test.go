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
	"net"
	"sync"
	"testing"
	"time"

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/net/multiplex"
	"github.com/containerd/nri/pkg/stub"
	"github.com/containerd/ttrpc"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/cpuset"
)

// reconnectingNRIRuntime speaks NRI's multiplexed ttrpc protocol and can drop
// the active connection without restarting the CPUDriver.
type reconnectingNRIRuntime struct {
	mu          sync.Mutex
	connection  net.Conn
	syncResults chan error
}

func (r *reconnectingNRIRuntime) dial(string) (net.Conn, error) {
	pluginConn, runtimeConn := net.Pipe()
	r.mu.Lock()
	r.connection = runtimeConn
	r.mu.Unlock()
	go r.serve(runtimeConn)
	return pluginConn, nil
}

func (r *reconnectingNRIRuntime) disconnect() {
	r.mu.Lock()
	conn := r.connection
	r.connection = nil
	r.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (r *reconnectingNRIRuntime) serve(conn net.Conn) {
	mux := multiplex.Multiplex(conn, multiplex.WithBlockedRead())
	defer mux.Close()

	pluginConn, err := mux.Open(multiplex.PluginServiceConn)
	if err != nil {
		return
	}
	plugin := api.NewPluginClient(ttrpc.NewClient(pluginConn))
	runtimeListener, err := mux.Listen(multiplex.RuntimeServiceConn)
	if err != nil {
		return
	}

	server, err := ttrpc.NewServer()
	if err != nil {
		return
	}
	api.RegisterRuntimeService(server, &reconnectingNRIRuntimeService{plugin: plugin, syncResults: r.syncResults})
	// Register both logical connections before accepting incoming frames.
	mux.Unblock()
	_ = server.Serve(context.Background(), runtimeListener)
}

type reconnectingNRIRuntimeService struct {
	plugin      api.PluginService
	syncResults chan error
}

func (s *reconnectingNRIRuntimeService) RegisterPlugin(ctx context.Context, _ *api.RegisterPluginRequest) (*api.Empty, error) {
	go func() {
		requestContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := s.plugin.Configure(requestContext, &api.ConfigureRequest{
			RuntimeName:         "test-runtime",
			RuntimeVersion:      "1.0",
			RegistrationTimeout: 1000,
			RequestTimeout:      1000,
		}); err != nil {
			s.syncResults <- err
			return
		}
		_, err := s.plugin.Synchronize(requestContext, &api.SynchronizeRequest{})
		s.syncResults <- err
	}()
	return &api.Empty{}, nil
}

func (s *reconnectingNRIRuntimeService) UpdateContainers(context.Context, *api.UpdateContainersRequest) (*api.UpdateContainersResponse, error) {
	return &api.UpdateContainersResponse{}, nil
}

func TestNRIRuntimeReconnectRetainsPreparedClaim(t *testing.T) {
	d, _ := newMetricsTestDriver(t)
	ctx, cancel := context.WithCancel(t.Context())
	runtime := &reconnectingNRIRuntime{syncResults: make(chan error, 4)}
	plugin, err := stub.New(d,
		stub.WithPluginName(d.driverName),
		stub.WithPluginIdx("00"),
		stub.WithDialer(runtime.dial),
		// An OnClose callback prevents the stub from exiting the test process.
		stub.WithOnClose(func() {}),
	)
	require.NoError(t, err)
	runDone := make(chan error, 1)
	go func() { runDone <- runNRIPluginWithRetry(ctx, plugin, maxAttempts) }()
	t.Cleanup(func() {
		cancel()
		plugin.Stop()
		runtime.disconnect()
		select {
		case <-runDone:
		case <-time.After(5 * time.Second):
			t.Error("NRI plugin runner did not stop")
		}
	})
	requireNoNRIError(t, runtime.syncResults)
	require.True(t, d.hasSynchronized, "initial NRI synchronization did not complete")

	claim := individualMetricsClaim("pending", "cpudev0")
	prepared, err := d.PrepareResourceClaims(ctx, []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.NoError(t, prepared[claim.UID].Err)
	pod := &api.PodSandbox{Id: "sandbox", Uid: "pod"}
	container := &api.Container{
		Id: "old-container", Name: "app", PodSandboxId: pod.Id,
		Env: []string{"DRA_CPUSET_pending=0"},
	}
	_, _, err = d.CreateContainer(ctx, pod, container)
	require.NoError(t, err)
	_, err = d.StopContainer(ctx, pod, container)
	require.NoError(t, err)

	// Drop only the NRI connection; the CPUDriver and its stores remain alive.
	runtime.disconnect()
	requireNoNRIError(t, runtime.syncResults)

	gotCPUs, found := d.allocationState.GetResourceClaimAllocation(claim.UID)
	require.True(t, found, "reconnect discarded the prepared allocation")
	require.True(t, cpuset.New(0).Equals(gotCPUs))
	require.True(t, d.allocationState.IsOwner(claim.UID, types.UID(pod.Uid), container.Name), "reconnect discarded claim ownership")

	otherPod := &api.PodSandbox{Id: "other-sandbox", Uid: "other-pod"}
	_, _, err = d.CreateContainer(ctx, otherPod, &api.Container{
		Id: "other-container", Name: container.Name, Env: []string{"DRA_CPUSET_pending=0"},
	})
	require.Error(t, err, "another Pod must not take the retained claim")
	container.Id = "replacement-container"
	adjustment, _, err := d.CreateContainer(ctx, pod, container)
	require.NoError(t, err)
	require.Equal(t, "0", adjustment.Linux.Resources.Cpu.Cpus)
}

func requireNoNRIError(t *testing.T, results <-chan error) {
	t.Helper()
	select {
	case err := <-results:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for NRI synchronization")
	}
}
