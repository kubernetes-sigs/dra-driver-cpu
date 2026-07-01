/*
Copyright 2025 The Kubernetes Authors.

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

package e2e

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

func TestGetPodHealthzViaAPIProxyReturnsHTTP200(t *testing.T) {
	var requestPath string
	server := mustNewHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.EscapedPath()
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET request, got %s", r.Method)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	}))

	coreClient := mustNewTestCoreV1Client(t, server)
	pod := v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "driver-pod",
			Namespace: daemonSetNamespace,
		},
	}

	statusCode, body, err := getPodHealthzViaAPIProxy(context.Background(), coreClient, pod)
	if err != nil {
		t.Fatalf("expected success, got error: %v", err)
	}
	if statusCode != http.StatusOK {
		t.Fatalf("expected status 200, got %d", statusCode)
	}
	if body != "ok" {
		t.Fatalf("expected body %q, got %q", "ok", body)
	}
	const expectedPath = "/api/v1/namespaces/kube-system/pods/http:driver-pod:8080/proxy/healthz"
	if requestPath != expectedPath {
		t.Fatalf("expected request path %q, got %q", expectedPath, requestPath)
	}
}

func TestGetPodHealthzViaAPIProxyReturnsStatusOnFailure(t *testing.T) {
	server := mustNewHTTPTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))

	coreClient := mustNewTestCoreV1Client(t, server)
	pod := v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "driver-pod",
			Namespace: daemonSetNamespace,
		},
	}

	statusCode, body, err := getPodHealthzViaAPIProxy(context.Background(), coreClient, pod)
	if err == nil {
		t.Fatal("expected an error for a non-2xx response")
	}
	if statusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", statusCode)
	}
	if body != "" {
		t.Fatalf("expected empty body on error, got %q", body)
	}
}

func mustNewTestCoreV1Client(t *testing.T, server *httptest.Server) *typedcorev1.CoreV1Client {
	t.Helper()

	gv := schema.GroupVersion{Version: "v1"}
	coreClient, err := typedcorev1.NewForConfigAndClient(&rest.Config{
		Host:    server.URL,
		APIPath: "/api",
		ContentConfig: rest.ContentConfig{
			GroupVersion:         &gv,
			NegotiatedSerializer: scheme.Codecs.WithoutConversion(),
		},
	}, server.Client())
	if err != nil {
		t.Fatalf("creating core v1 client: %v", err)
	}
	return coreClient
}

func mustNewHTTPTestServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("creating test listener: %v", err)
	}

	server := httptest.NewUnstartedServer(handler)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)
	return server
}
