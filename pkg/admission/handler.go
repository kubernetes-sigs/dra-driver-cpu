/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use it except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
)

const (
	maxBodyBytes                  = 1 << 20
	defaultAdmissionReviewTimeout = 8 * time.Second
	defaultClaimGetRetryWait      = 50 * time.Millisecond
)

// Handler serves validating admission webhook requests and implements ClaimCPUCountGetter
// for pod validation by fetching ResourceClaims from the API server.
type Handler struct {
	driverName         string
	clientset          kubernetes.Interface
	claimGetRetryWait  time.Duration
	claimGetRetryTotal time.Duration
}

// NewHandler returns an http.Handler that validates admission reviews for ResourceClaims and Pods.
func NewHandler(driverName string, clientset kubernetes.Interface, claimGetRetryWait, claimGetRetryTotal time.Duration) *Handler {
	return &Handler{
		driverName:         driverName,
		clientset:          clientset,
		claimGetRetryWait:  claimGetRetryWait,
		claimGetRetryTotal: claimGetRetryTotal,
	}
}

// ServeHTTP validates admission review POSTs and writes the response.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "only POST is supported")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to read request body: %v", err))
		return
	}
	if len(body) == 0 {
		writeError(w, http.StatusBadRequest, "empty request body")
		return
	}

	var review admissionv1.AdmissionReview
	if err := json.Unmarshal(body, &review); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("failed to parse admission review: %v", err))
		return
	}
	if review.Request == nil {
		writeError(w, http.StatusBadRequest, "admission review request is nil")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), defaultAdmissionReviewTimeout)
	defer cancel()
	response := h.handleReview(ctx, review.Request)
	response.UID = review.Request.UID
	review.Response = response
	review.TypeMeta = metav1.TypeMeta{APIVersion: "admission.k8s.io/v1", Kind: "AdmissionReview"}

	respBytes, err := json.Marshal(review)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("failed to serialize admission response: %v", err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(respBytes)
}

// ClaimCPUCount implements ClaimCPUCountGetter for pod validation.
func (h *Handler) ClaimCPUCount(ctx context.Context, namespace, claimName string) (int64, error) {
	return h.claimCPUCount(ctx, namespace, claimName)
}

func (h *Handler) handleReview(ctx context.Context, req *admissionv1.AdmissionRequest) *admissionv1.AdmissionResponse {
	if req.Operation != admissionv1.Create && req.Operation != admissionv1.Update {
		return &admissionv1.AdmissionResponse{Allowed: true}
	}

	if req.Kind.Group == "resource.k8s.io" && req.Kind.Kind == "ResourceClaim" {
		var claim resourceapi.ResourceClaim
		if err := json.Unmarshal(req.Object.Raw, &claim); err != nil {
			return deny(fmt.Sprintf("failed to decode ResourceClaim: %v", err))
		}
		errs := ValidateResourceClaim(&claim, h.driverName)
		if len(errs) == 0 {
			return &admissionv1.AdmissionResponse{Allowed: true}
		}
		return deny(strings.Join(errs, "; "))
	}

	if req.Kind.Group != "" || req.Kind.Kind != "Pod" {
		return &admissionv1.AdmissionResponse{Allowed: true}
	}

	var pod corev1.Pod
	if err := json.Unmarshal(req.Object.Raw, &pod); err != nil {
		return deny(fmt.Sprintf("failed to decode Pod: %v", err))
	}
	errs := ValidatePodClaims(ctx, &pod, h.driverName, h)
	if len(errs) > 0 {
		return deny(strings.Join(errs, "; "))
	}
	return &admissionv1.AdmissionResponse{Allowed: true}
}

// isRetryableClaimGetError returns true for errors that may succeed on retry
// (claim not yet created, transient API issues).
func isRetryableClaimGetError(err error) bool {
	return apierrors.IsNotFound(err) ||
		apierrors.IsServerTimeout(err) ||
		apierrors.IsTimeout(err)
}

func (h *Handler) claimCPUCount(ctx context.Context, namespace, name string) (int64, error) {
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
	retryDeadline := time.Now().Add(totalWait)
	if ctxDeadline, ok := ctx.Deadline(); ok && ctxDeadline.Before(retryDeadline) {
		retryDeadline = ctxDeadline
	}
	var timer *time.Timer
	for {
		claim, err = h.clientset.ResourceV1().ResourceClaims(namespace).Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			break
		}
		if !isRetryableClaimGetError(err) {
			return 0, err
		}
		if time.Now().After(retryDeadline) {
			return 0, nil
		}
		// Sleep duration is retryWait (default 50ms), capped by time remaining until retryDeadline.
		sleepFor := retryWait
		if remaining := time.Until(retryDeadline); remaining < sleepFor {
			sleepFor = remaining
		}
		if sleepFor <= 0 {
			return 0, nil
		}
		if timer == nil {
			timer = time.NewTimer(sleepFor)
			defer timer.Stop()
		} else {
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(sleepFor)
		}
		// Wait for retry delay or context cancel;
		// timer.C fires when the sleep duration elapses.
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-timer.C:
		}
	}

	if claim.Status.Allocation != nil {
		return 0, ErrClaimAlreadyAllocated
	}
	return ClaimCPUCountFromSpec(claim, h.driverName), nil
}

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

func writeError(w http.ResponseWriter, status int, message string) {
	klog.Warning(message)
	http.Error(w, message, status)
}
