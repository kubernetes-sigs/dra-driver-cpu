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
	"testing"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestHandler_ValidatePodClaimsWiring ensures the Handler implements ClaimCPUCountGetter
// and that ValidatePodClaims works when called with it (claim fetched via clientset).
func TestHandler_ValidatePodClaimsWiring(t *testing.T) {
	claim := &resourceapi.ResourceClaim{ //nolint:exhaustruct
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "claim-4"},
		Spec: resourceapi.ResourceClaimSpec{
			Devices: resourceapi.DeviceClaim{
				Requests: []resourceapi.DeviceRequest{
					{
						Name:    "req",
						Exactly: &resourceapi.ExactDeviceRequest{DeviceClassName: DefaultDriverName, Count: 4},
					},
				},
			},
		},
	}
	clientset := fake.NewSimpleClientset(claim)
	handler := NewHandler(DefaultDriverName, clientset, 0, 0)

	pod := &corev1.Pod{ //nolint:exhaustruct
		ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "pod-ok"},
		Spec: corev1.PodSpec{
			ResourceClaims: []corev1.PodResourceClaim{
				{Name: "claim-ref", ResourceClaimName: strPtr("claim-4")},
			},
			Containers: []corev1.Container{
				{
					Name: "main",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
						Claims:   []corev1.ResourceClaim{{Name: "claim-ref"}},
					},
				},
			},
		},
	}

	errs := ValidatePodClaims(context.Background(), pod, DefaultDriverName, handler)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}
