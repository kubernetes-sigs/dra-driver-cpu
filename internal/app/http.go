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
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

const httpShutdownTimeout = 5 * time.Second

type httpServer struct {
	listener  net.Listener
	server    *http.Server
	ready     atomic.Bool
	serveDone chan struct{}
	serveErr  chan error
}

func startHTTPServer(listen func(string, string) (net.Listener, error), address string, metrics Metrics) (*httpServer, error) {
	listener, err := listen("tcp", address)
	if err != nil {
		return nil, err
	}

	s := &httpServer{
		listener:  listener,
		serveDone: make(chan struct{}),
		serveErr:  make(chan error, 1),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		if !s.ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	metricsHandler := promhttp.InstrumentMetricHandler(
		metrics.Registerer,
		promhttp.HandlerFor(metrics.Gatherer, promhttp.HandlerOpts{}),
	)
	mux.Handle("/metrics", metricsHandler)
	s.server = &http.Server{
		Handler:           mux,
		IdleTimeout:       120 * time.Second,
		ReadTimeout:       10 * time.Second,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
	}

	go func() {
		defer close(s.serveDone)
		defer close(s.serveErr)
		if err := s.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.serveErr <- err
		}
	}()

	return s, nil
}

func (s *httpServer) setReady(ready bool) {
	s.ready.Store(ready)
}

func (s *httpServer) errors() <-chan error {
	return s.serveErr
}

func (s *httpServer) shutdown(timeout time.Duration) error {
	s.ready.Store(false)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	shutdownErr := s.server.Shutdown(ctx)
	if shutdownErr != nil {
		shutdownErr = errors.Join(shutdownErr, s.server.Close())
	}
	closeErr := s.listener.Close()
	if errors.Is(closeErr, net.ErrClosed) {
		closeErr = nil
	}

	var serveErr error
	select {
	case <-s.serveDone:
	case <-ctx.Done():
		serveErr = fmt.Errorf("wait for HTTP server to stop: %w", ctx.Err())
	}
	return errors.Join(shutdownErr, closeErr, serveErr)
}
