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
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestHTTPServerReadiness(t *testing.T) {
	listener := newFakeListener()
	registry := prometheus.NewRegistry()
	server, err := startHTTPServer(
		func(string, string) (net.Listener, error) { return listener, nil },
		":8080",
		Metrics{Registerer: registry, Gatherer: registry},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.shutdown(time.Second); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})

	assertStatus(t, server, http.StatusServiceUnavailable)
	server.setReady(true)
	assertStatus(t, server, http.StatusOK)
	if err := server.shutdown(time.Second); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	assertStatus(t, server, http.StatusServiceUnavailable)
	if err, ok := <-server.errors(); ok {
		t.Fatalf("shutdown reported serve error: %v", err)
	}
	select {
	case <-server.serveDone:
	default:
		t.Fatal("shutdown returned before the serve goroutine stopped")
	}
}

func TestHTTPServerUsesInjectedMetricsRegistry(t *testing.T) {
	listener := newFakeListener()
	registry := prometheus.NewRegistry()
	gauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_metric"})
	registry.MustRegister(gauge)
	gauge.Set(1)

	server, err := startHTTPServer(
		func(string, string) (net.Listener, error) { return listener, nil },
		":8080",
		Metrics{Registerer: registry, Gatherer: registry},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.shutdown(time.Second); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})

	firstResponse := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(firstResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if firstResponse.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want %d", firstResponse.Code, http.StatusOK)
	}

	secondResponse := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(secondResponse, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("metrics status = %d, want %d", secondResponse.Code, http.StatusOK)
	}
	body := secondResponse.Body.String()
	if !strings.Contains(body, "test_metric 1") {
		t.Fatalf("metrics body does not contain injected metric:\n%s", body)
	}
	if !strings.Contains(body, "promhttp_metric_handler_requests_in_flight") {
		t.Fatalf("metrics body does not contain promhttp in-flight metric:\n%s", body)
	}
	if !strings.Contains(body, "promhttp_metric_handler_requests_total") {
		t.Fatalf("metrics body does not contain promhttp request counter:\n%s", body)
	}
}

func TestHTTPServerBindError(t *testing.T) {
	wantErr := errors.New("bind failed")
	registry := prometheus.NewRegistry()
	server, err := startHTTPServer(
		func(string, string) (net.Listener, error) { return nil, wantErr },
		":8080",
		Metrics{Registerer: registry, Gatherer: registry},
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("startHTTPServer() error = %v, want %v", err, wantErr)
	}
	if server != nil {
		t.Fatal("startHTTPServer() returned a server after bind failure")
	}
}

func TestHTTPServerReportsServeError(t *testing.T) {
	wantErr := errors.New("serve failed")
	listener := newFakeListener()
	listener.acceptErr = wantErr
	registry := prometheus.NewRegistry()
	server, err := startHTTPServer(
		func(string, string) (net.Listener, error) { return listener, nil },
		":8080",
		Metrics{Registerer: registry, Gatherer: registry},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := server.shutdown(time.Second); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})

	select {
	case err, ok := <-server.errors():
		if !ok {
			t.Fatal("HTTP server error channel closed without an error")
		}
		if !errors.Is(err, wantErr) {
			t.Fatalf("HTTP server error = %v, want %v", err, wantErr)
		}
	case <-time.After(testWaitTimeout):
		t.Fatal("HTTP server did not report its serve error")
	}
}

func TestMonitorHTTPServerCancelsContext(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	t.Cleanup(func() {
		cancel(nil)
	})

	wantErr := errors.New("serve failed")
	httpErr := make(chan error, 1)
	httpErr <- wantErr

	monitorHTTPServer(ctx, cancel, httpErr)
	if cause := context.Cause(ctx); !errors.Is(cause, wantErr) {
		t.Fatalf("context cause = %v, want %v", cause, wantErr)
	}
}

func assertStatus(t *testing.T, server *httpServer, want int) {
	t.Helper()

	response := httptest.NewRecorder()
	server.server.Handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != want {
		t.Fatalf("healthz status = %d, want %d", response.Code, want)
	}
}
