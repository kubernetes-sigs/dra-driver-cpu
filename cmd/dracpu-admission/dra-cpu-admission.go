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
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"time"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/admission"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

const (
	defaultBindAddress            = ":9443"
	maxBodyBytes                  = 1 << 20
	defaultClaimGetRetryWait      = 50 * time.Millisecond
	defaultClaimGetRetryTotal     = 500 * time.Millisecond
	defaultAdmissionReviewTimeout = 8 * time.Second
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
	healthzStatus      atomicBool
)

type atomicBool struct{ v int32 }

// Load returns the current boolean value. Returns: current state.
func (b *atomicBool) Load() bool {
	return atomic.LoadInt32(&b.v) == 1
}

// Store updates the current boolean value. Returns: nothing.
func (b *atomicBool) Store(value bool) {
	if value {
		atomic.StoreInt32(&b.v, 1)
		return
	}
	atomic.StoreInt32(&b.v, 0)
}

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
	claimGetRetryWait = durationFromEnv("DRACPU_ADMISSION_CLAIM_GET_RETRY_WAIT", claimGetRetryWait)
	claimGetRetryTotal = durationFromEnv("DRACPU_ADMISSION_CLAIM_GET_RETRY_TOTAL", claimGetRetryTotal)

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
	mux.Handle("/validate", newAdmissionHandler(driverName, clientset))

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

type admissionHandler struct {
	driverName         string
	clientset          kubernetes.Interface
	claimGetRetryWait  time.Duration
	claimGetRetryTotal time.Duration
}

// newAdmissionHandler constructs an HTTP handler with driver configuration. Returns: handler.
func newAdmissionHandler(driverName string, clientset kubernetes.Interface) http.Handler {
	return &admissionHandler{
		driverName:         driverName,
		clientset:          clientset,
		claimGetRetryWait:  claimGetRetryWait,
		claimGetRetryTotal: claimGetRetryTotal,
	}
}

// ServeHTTP validates admission review POSTs and writes the response. Returns: nothing.
func (h *admissionHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only AdmissionReview POSTs are supported.
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}

	// Read and cap the request body size to prevent abuse.
	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to read request body: %v", err))
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "empty request body")
		return
	}

	// Decode the AdmissionReview request.
	var review admissionv1.AdmissionReview
	if err := json.Unmarshal(body, &review); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to parse admission review: %v", err))
		return
	}
	if review.Request == nil {
		writeError(w, http.StatusBadRequest, "admission review request is nil")
		return
	}

	// Use a single request-scoped context with timeout for the whole validation chain so
	// all API calls and retries respect the admission timeout.
	ctx, cancel := context.WithTimeout(r.Context(), defaultAdmissionReviewTimeout)
	defer cancel()
	response := h.handleReview(ctx, review.Request)
	response.UID = review.Request.UID
	review.Response = response
	review.TypeMeta = metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"}

	// Return the AdmissionReview response payload.
	respBytes, err := json.Marshal(review)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to serialize admission response: %v", err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respBytes)
}

// handleReview routes admission validation by resource type and operation. Returns: AdmissionResponse.
func (h *admissionHandler) handleReview(ctx context.Context, req *admissionv1.AdmissionRequest) *admissionv1.AdmissionResponse {
	// Only validate create and update requests.
	if req.Operation != admissionv1.Create && req.Operation != admissionv1.Update {
		return &admissionv1.AdmissionResponse{Allowed: true}
	}

	// Validate ResourceClaims for the configured DRA driver.
	if req.Kind.Group == "resource.k8s.io" && req.Kind.Kind == "ResourceClaim" {
		var claim resourceapi.ResourceClaim
		if err := json.Unmarshal(req.Object.Raw, &claim); err != nil {
			return deny(fmt.Sprintf("failed to decode ResourceClaim: %v", err))
		}

		errs := admission.ValidateResourceClaim(&claim, h.driverName)
		if len(errs) == 0 {
			return &admissionv1.AdmissionResponse{Allowed: true}
		}
		return deny(strings.Join(errs, "; "))
	}

	// Ignore unrelated resources.
	if req.Kind.Group != "" || req.Kind.Kind != "Pod" {
		return &admissionv1.AdmissionResponse{Allowed: true}
	}

	// Validate pod CPU requests against referenced dra.cpu claims.
	var pod corev1.Pod
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		return deny(fmt.Sprintf("failed to decode Pod: %v", err))
	}

	errs := admission.ValidatePodClaims(ctx, &pod, h.driverName, h)
	if len(errs) > 0 {
		return deny(strings.Join(errs, "; "))
	}

	return &admissionv1.AdmissionResponse{Allowed: true}
}

// deny formats a consistent invalid response for admission failures. Returns: AdmissionResponse.
func deny(message string) *admissionv1.AdmissionResponse {
	return &admissionv1.AdmissionResponse{
		Allowed: false,
		Result: &metav1.Status{
			Status:  metav1.StatusFailure,
			Message: message,
			Reason:  metav1.StatusReasonInvalid,
		},
	}
}

// writeError sends a simple HTTP error response and logs the issue. Returns: nothing.
func writeError(w http.ResponseWriter, status int, message string) {
	klog.Warning(message)
	http.Error(w, message, status)
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

// ClaimCPUCount implements admission.ClaimCPUCountGetter for pod validation.
func (h *admissionHandler) ClaimCPUCount(ctx context.Context, namespace, claimName string) (int64, error) {
	return h.claimCPUCount(ctx, namespace, claimName)
}

// claimCPUCount totals dra.cpu device requests within a ResourceClaim. Returns: total, error.
// It uses the same ctx as the rest of the admission chain so retries and API calls respect the request timeout.
func (h *admissionHandler) claimCPUCount(ctx context.Context, namespace, name string) (int64, error) {
	// Fetch the ResourceClaim and sum CPU requests for this driver.
	// Claims can be created asynchronously (for example from Pod claim templates),
	// so retry briefly on NotFound before treating the claim as not yet available.
	var claim *resourceapi.ResourceClaim
	var err error
	totalWait := h.claimGetRetryTotal
	if totalWait < 0 {
		totalWait = 0
	}
	retryWait := h.claimGetRetryWait
	if retryWait <= 0 {
		retryWait = defaultClaimGetRetryWait
	}
	// Bound retry deadline by the request context so we don't retry past the admission timeout.
	retryDeadline := time.Now().Add(totalWait)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(retryDeadline) {
		retryDeadline = ctxDeadline
	}
	for {
		claim, err = h.clientset.ResourceV1().ResourceClaims(namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			break
		}
		if !apierrors.IsNotFound(err) {
			return 0, err
		}
		if time.Now().After(retryDeadline) {
			return 0, nil
		}
		sleepFor := retryWait
		if remaining := time.Until(retryDeadline); remaining < sleepFor {
			sleepFor = remaining
		}
		if sleepFor <= 0 {
			return 0, nil
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(sleepFor):
		}
	}

	// Reject pods that reference a claim already allocated (e.g. to another pod).
	if claim.Status.Allocation != nil {
		return 0, admission.ErrClaimAlreadyAllocated
	}

	return admission.ClaimCPUCountFromSpec(claim, h.driverName), nil
}

func durationFromEnv(name string, fallback time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	value, err := time.ParseDuration(raw)
	if err != nil {
		klog.Warningf("invalid %s=%q: %v; using %s", name, raw, err, fallback)
		return fallback
	}
	return value
}
