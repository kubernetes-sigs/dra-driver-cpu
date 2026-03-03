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

package main

import (
	"context"
	"crypto/tls"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"time"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/admission"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

const (
	defaultBindAddress        = ":9443"
	defaultClaimGetRetryWait  = 50 * time.Millisecond
	defaultClaimGetRetryTotal = 500 * time.Millisecond
)

var (
	bindAddress        string
	tlsCertFile        string
	tlsKeyFile         string
	kubeconfig         string
	driverName         string
	healthzPath        string
	claimGetRetryWait  time.Duration
	claimGetRetryTotal time.Duration
	healthzStatus      atomic.Bool
)

// init wires CLI flags for runtime configuration. Returns: nothing.
func init() {
	// Wire CLI flags for TLS, bind address, and driver-specific behavior.
	flag.StringVar(&bindAddress, "bind-address", defaultBindAddress, "The address to bind the webhook server")
	flag.StringVar(&tlsCertFile, "tls-cert-file", "/etc/webhook/certs/tls.crt", "Path to the TLS certificate file")
	flag.StringVar(&tlsKeyFile, "tls-private-key-file", "/etc/webhook/certs/tls.key", "Path to the TLS private key file")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Absolute path to the kubeconfig file (optional)")
	flag.StringVar(&driverName, "driver-name", admission.DefaultDriverName, "DRA driver name to validate for")
	flag.StringVar(&healthzPath, "healthz-path", "/healthz", "Health check path")
	flag.DurationVar(&claimGetRetryWait, "claim-get-retry-wait", defaultClaimGetRetryWait, "Delay between ResourceClaim get retries when claim is not found")
	flag.DurationVar(&claimGetRetryTotal, "claim-get-retry-total", defaultClaimGetRetryTotal, "Total ResourceClaim get retry window when claim is not found")
}

// main initializes the admission webhook server and runs until shutdown. Returns: nothing.
func main() {
	klog.InitFlags(nil)
	flag.Parse()

	// Create a client for fetching ResourceClaims referenced by pods.
	clientset, err := newClientset(kubeconfig)
	if err != nil {
		klog.Fatalf("failed to create kubernetes client: %v", err)
	}

	mux := http.NewServeMux()
	// Expose a lightweight readiness endpoint for probes.
	mux.HandleFunc(healthzPath, func(w http.ResponseWriter, r *http.Request) {
		if !healthzStatus.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})

	// Handle admission review requests at a single webhook path.
	mux.Handle("/validate", admission.NewHandler(driverName, clientset, claimGetRetryWait, claimGetRetryTotal))

	// Configure the HTTPS webhook server.
	server := &http.Server{
		Addr:              bindAddress,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}

	go func() {
		klog.Infof("Starting validating admission server on %s", bindAddress)
		listener, err := tls.Listen("tcp", server.Addr, mustTLSConfig(tlsCertFile, tlsKeyFile))
		if err != nil {
			klog.Fatalf("Webhook server listen failed: %v", err)
		}
		healthzStatus.Store(true)
		if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
			klog.Fatalf("Webhook server failed: %v", err)
		}
	}()

	// Wait for a signal and perform a graceful shutdown.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	<-ctx.Done()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer shutdownCancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		klog.Errorf("Webhook server shutdown failed: %v", err)
	}
}

// newClientset builds a client for accessing ResourceClaim objects. Returns: client or error.
func newClientset(kubeconfigPath string) (kubernetes.Interface, error) {
	var config *rest.Config
	var err error
	if kubeconfigPath != "" {
		// Use an explicit kubeconfig when provided.
		config, err = clientcmd.BuildConfigFromFlags("", kubeconfigPath)
	} else {
		// Fall back to in-cluster config.
		config, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, err
	}
	return kubernetes.NewForConfig(config)
}

// mustTLSConfig loads cert and key and returns a TLS config for the server. Exits on error.
func mustTLSConfig(certFile, keyFile string) *tls.Config {
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		klog.Fatalf("failed to load TLS cert/key: %v", err)
	}
	return &tls.Config{Certificates: []tls.Certificate{cert}}
}
