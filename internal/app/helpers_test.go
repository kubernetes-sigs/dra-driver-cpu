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

package app

import (
	"context"
	"net"
	"sync"
	"sync/atomic"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/driverconfig"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/driver"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/sysfs"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

type callLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *callLog) add(call string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, call)
}

func (l *callLog) entries() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

type fakeListener struct {
	closed      chan struct{}
	closeOnce   sync.Once
	acceptErr   error
	acceptErrCh chan error
	closeErr    error
	onClose     func()
}

func newFakeListener() *fakeListener {
	return &fakeListener{closed: make(chan struct{})}
}

func (l *fakeListener) Accept() (net.Conn, error) {
	if l.acceptErr != nil {
		return nil, l.acceptErr
	}
	if l.acceptErrCh != nil {
		select {
		case err := <-l.acceptErrCh:
			return nil, err
		case <-l.closed:
			return nil, net.ErrClosed
		}
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *fakeListener) Close() error {
	var err error
	l.closeOnce.Do(func() {
		close(l.closed)
		err = l.closeErr
		if l.onClose != nil {
			l.onClose()
		}
	})
	return err
}

func (l *fakeListener) Addr() net.Addr {
	return fakeAddr("test")
}

func (l *fakeListener) isClosed() bool {
	select {
	case <-l.closed:
		return true
	default:
		return false
	}
}

type fakeAddr string

func (fakeAddr) Network() string {
	return "tcp"
}

func (a fakeAddr) String() string {
	return string(a)
}

type fakeDriver struct {
	startErr  error
	asyncErr  chan error
	started   chan struct{}
	startOnce sync.Once
	stopCalls atomic.Int32
	onStop    func()
}

func newFakeDriver() *fakeDriver {
	return &fakeDriver{
		asyncErr: make(chan error, 1),
		started:  make(chan struct{}),
	}
}

func (d *fakeDriver) Start(context.Context) (<-chan error, error) {
	d.startOnce.Do(func() {
		close(d.started)
	})
	if d.startErr != nil {
		return nil, d.startErr
	}
	return d.asyncErr, nil
}

func (d *fakeDriver) Stop() {
	d.stopCalls.Add(1)
	if d.onStop != nil {
		d.onStop()
	}
}

type appTestEnv struct {
	deps     Dependencies
	calls    *callLog
	listener *fakeListener
	driver   *fakeDriver
}

func newAppTestEnv() *appTestEnv {
	calls := &callLog{}
	listener := newFakeListener()
	dracpu := newFakeDriver()
	registry := prometheus.NewRegistry()
	listener.onClose = func() {
		calls.add("http.close")
	}
	dracpu.onStop = func() {
		calls.add("driver.stop")
	}

	env := &appTestEnv{
		calls:    calls,
		listener: listener,
		driver:   dracpu,
	}
	env.deps = Dependencies{
		Listen: func(network, address string) (net.Listener, error) {
			calls.add("listen")
			return listener, nil
		},
		NewSysFS: func(logr.Logger, string) (sysfs.FS, error) {
			calls.add("sysfs")
			return sysfs.Host(), nil
		},
		NewKubeClient: func(driverconfig.Config) (kubernetes.Interface, error) {
			calls.add("kube")
			return fake.NewSimpleClientset(), nil
		},
		NewNodeName: func(string) (string, error) {
			calls.add("node")
			return "test-node", nil
		},
		NewDriver: func(logr.Logger, driver.Providers, *driver.Config) (Driver, error) {
			calls.add("driver.new")
			return dracpu, nil
		},
		Metrics: Metrics{
			Registerer: registry,
			Gatherer:   registry,
		},
	}
	return env
}
