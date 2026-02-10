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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"
)

const (
	defaultBindAddress = ":9443"
	maxBodyBytes       = 1 << 20
)

var (
	bindAddress   string
	tlsCertFile   string
	tlsKeyFile    string
	kubeconfig    string
	driverName    string
	healthzPath   string
	healthzStatus atomicBool
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
	mux.Handle("/validate", newAdmissionHandler(driverName, clientset))

	// Configure the HTTPS webhook server.
	server := &http.Server{
		Addr:              bindAddress,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
	}

	healthzStatus.Store(true)

	go func() {
		// Start serving TLS requests until shutdown is triggered.
		klog.Infof("Starting validating admission server on %s", bindAddress)
		if err := server.ListenAndServeTLS(tlsCertFile, tlsKeyFile); err != nil && err != http.ErrServerClosed {
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
	driverName string
	clientset  kubernetes.Interface
}

// newAdmissionHandler constructs an HTTP handler with driver configuration. Returns: handler.
func newAdmissionHandler(driverName string, clientset kubernetes.Interface) http.Handler {
	return &admissionHandler{driverName: driverName, clientset: clientset}
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

	// Dispatch validation and attach the response to the review.
	response := h.handleReview(review.Request)
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
func (h *admissionHandler) handleReview(req *admissionv1.AdmissionRequest) *admissionv1.AdmissionResponse {
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

	errs := h.validatePodClaims(&pod)
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

// validatePodClaims enforces CPU request parity with dra.cpu claims. Returns: validation errors.
func (h *admissionHandler) validatePodClaims(pod *corev1.Pod) []string {

	// No accounting needed for empty pods or those without DRA claims.
	if pod == nil || len(pod.Spec.ResourceClaims) == 0 {
		return nil
	}
	// Build a map from pod claim references to actual ResourceClaim names.
	claimNameToResource := make(map[string]string)
	for _, rc := range pod.Spec.ResourceClaims {
		if rc.Name == "" {
			continue
		}
		if rc.ResourceClaimName != nil {
			claimNameToResource[rc.Name] = *rc.ResourceClaimName
		}
	}

	if len(claimNameToResource) == 0 {
		return nil
	}

	var errs []string
	for _, container := range pod.Spec.Containers {
		// Extract the integer CPU request for this container.
		cpuRequestValue, cpuSpecified := cpuRequestCount(container.Resources.Requests)
		// Sum the CPU count across all dra.cpu claims referenced by the container.
		totalClaimCPUs := int64(0)
		for _, claim := range container.Resources.Claims {
			resourceClaimName, ok := claimNameToResource[claim.Name]
			if !ok {
				continue
			}
			claimCPUs, err := h.claimCPUCount(pod.Namespace, resourceClaimName)
			if err != nil {
				errs = append(errs, fmt.Sprintf("container %q: failed to get ResourceClaim %q: %v", container.Name, resourceClaimName, err))
				continue
			}
			totalClaimCPUs += claimCPUs
		}

		// Skip containers that define neither a CPU request nor a dra.cpu claim.
		if !cpuSpecified && totalClaimCPUs == 0 {
			continue
		}

		// If only dra.cpu claims are present, skip comparison.
		if totalClaimCPUs > 0 && !cpuSpecified {
			continue
		}

		// Enforce exact match to claim CPU count.
		if totalClaimCPUs > 0 && cpuRequestValue != totalClaimCPUs {
			errs = append(errs, fmt.Sprintf("container %q: expected %d CPU cores from dra.cpu claims, got %d in cpu requests", container.Name, totalClaimCPUs, cpuRequestValue))
		}
	}

	return errs
}

// cpuRequestCount parses CPU requests into integer count plus flags. Returns: count, specified.
func cpuRequestCount(requests corev1.ResourceList) (int64, bool) {
	// Return request count and whether CPU was specified.
	cpuQuantity, ok := requests[corev1.ResourceCPU]
	if !ok {
		return 0, false
	}
	value, ok := cpuQuantity.AsInt64()
	if !ok {
		// Round any fractional CPU request up to 1.
		return 1, true
	}
	if value < 1 {
		return 1, true
	}
	intQuantity := resource.NewQuantity(value, cpuQuantity.Format)
	if cpuQuantity.Cmp(*intQuantity) != 0 {
		// Round any fractional CPU request up to 1.
		return 1, true
	}
	return value, true
}

// claimCPUCount totals dra.cpu device requests within a ResourceClaim. Returns: total, error.
func (h *admissionHandler) claimCPUCount(namespace, name string) (int64, error) {
	// Fetch the ResourceClaim and sum CPU requests for this driver.
	claim, err := h.clientset.ResourceV1().ResourceClaims(namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		return 0, err
	}

	// Prefer allocated device info when available.
	if total, err := h.claimCPUCountFromSlices(claim); err != nil || total > 0 {
		return total, err
	}

	var total int64
	for _, request := range claim.Spec.Devices.Requests {
		if request.Exactly == nil || request.Exactly.DeviceClassName != h.driverName {
			continue
		}
		total += exactRequestCPUCount(request.Exactly)
	}

	return total, nil
}

// claimCPUCountFromSlices counts CPU total by looking up allocated devices in ResourceSlices. Returns: total, error.
func (h *admissionHandler) claimCPUCountFromSlices(claim *resourceapi.ResourceClaim) (int64, error) {
	if claim == nil || claim.Status.Allocation == nil || len(claim.Status.Allocation.Devices.Results) == 0 {
		return 0, nil
	}

	deviceNames := make(map[string]struct{})
	for _, result := range claim.Status.Allocation.Devices.Results {
		if result.Driver != h.driverName {
			continue
		}
		if result.Device == "" {
			continue
		}
		deviceNames[result.Device] = struct{}{}
	}
	if len(deviceNames) == 0 {
		return 0, nil
	}

	selector := fields.SelectorFromSet(fields.Set{
		resourceapi.ResourceSliceSelectorDriver: h.driverName,
	})
	slices, err := h.clientset.ResourceV1().ResourceSlices().List(context.Background(), metav1.ListOptions{
		FieldSelector: selector.String(),
	})
	if err != nil {
		return 0, err
	}

	deviceCapacities := make(map[string]map[resourceapi.QualifiedName]resourceapi.DeviceCapacity)
	for _, slice := range slices.Items {
		for _, device := range slice.Spec.Devices {
			deviceCapacities[device.Name] = device.Capacity
		}
	}

	var total int64
	for deviceName := range deviceNames {
		capacities, ok := deviceCapacities[deviceName]
		if !ok {
			continue
		}

		// If a grouped mode, the dra.cpu/cpu capacity will be defined; otherwise count one per device.
		if capacity, ok := capacities[resourceapi.QualifiedName("dra.cpu/cpu")]; ok {
			total += capacity.Value.Value()
			continue
		}
		total++
	}
	return total, nil
}

// exactRequestCPUCount determines the CPU count for a single request. Returns: count.
func exactRequestCPUCount(req *resourceapi.ExactDeviceRequest) int64 {
	// Prefer consumable capacity requests when present, otherwise use Count.
	if req == nil {
		return 0
	}
	count := req.Count
	if count < 1 {
		count = 1
	}
	if req.Capacity != nil && len(req.Capacity.Requests) > 0 {
		quantity, ok := req.Capacity.Requests[admission.CPUResourceQualifiedNameKey]
		if !ok {
			return 0
		}
		value, ok := quantity.AsInt64()
		if !ok || value < 1 {
			return 0
		}
		intQuantity := resource.NewQuantity(value, quantity.Format)
		if quantity.Cmp(*intQuantity) != 0 {
			return 0
		}
		return value * count
	}
	return count
}
