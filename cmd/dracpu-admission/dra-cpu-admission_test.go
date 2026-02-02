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
	"testing"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/admission"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// TestValidatePodClaims_CPURequestMatchesClaimCount ensures a matching request passes.
func TestValidatePodClaims_CPURequestMatchesClaimCount(t *testing.T) {
	clientset := fake.NewSimpleClientset(
		resourceClaim("default", "claim-4", exactCountRequest(4)),
	)

	handler := &admissionHandler{
		driverName: admission.DefaultDriverName,
		clientset:  clientset,
	}

	pod := podWithClaims("default", "pod-ok", "claim-ref", "claim-4")
	pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("4"),
	}

	errs := handler.validatePodClaims(pod)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

// TestValidatePodClaims_NoCPURequestWithClaimSucceeds allows claim-only containers.
func TestValidatePodClaims_NoCPURequestWithClaimSucceeds(t *testing.T) {
	clientset := fake.NewSimpleClientset(
		resourceClaim("default", "claim-2", exactCountRequest(2)),
	)

	handler := &admissionHandler{
		driverName: admission.DefaultDriverName,
		clientset:  clientset,
	}

	pod := podWithClaims("default", "pod-claim-only", "claim-ref", "claim-2")

	errs := handler.validatePodClaims(pod)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

// TestValidatePodClaims_NoCPUAndNoClaimSkipsValidation ensures no-op behavior.
func TestValidatePodClaims_NoCPUAndNoClaimSkipsValidation(t *testing.T) {
	handler := &admissionHandler{
		driverName: admission.DefaultDriverName,
		clientset:  fake.NewSimpleClientset(),
	}

	pod := &corev1.Pod{}

	errs := handler.validatePodClaims(pod)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

// TestValidatePodClaims_CPUMismatchRejected rejects mismatched CPU requests.
func TestValidatePodClaims_CPUMismatchRejected(t *testing.T) {
	clientset := fake.NewSimpleClientset(
		resourceClaim("default", "claim-4", exactCountRequest(4)),
	)

	handler := &admissionHandler{
		driverName: admission.DefaultDriverName,
		clientset:  clientset,
	}

	pod := podWithClaims("default", "pod-mismatch", "claim-ref", "claim-4")
	pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("2"),
	}

	errs := handler.validatePodClaims(pod)
	if len(errs) == 0 {
		t.Fatal("expected errors, got none")
	}
}

// TestValidatePodClaims_CPUQuantityMustBeInteger rejects fractional requests.
func TestValidatePodClaims_CPUQuantityMustBeInteger(t *testing.T) {
	clientset := fake.NewSimpleClientset(
		resourceClaim("default", "claim-2", exactCountRequest(2)),
	)

	handler := &admissionHandler{
		driverName: admission.DefaultDriverName,
		clientset:  clientset,
	}

	pod := podWithClaims("default", "pod-fractional", "claim-ref", "claim-2")
	pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("500m"),
	}

	errs := handler.validatePodClaims(pod)
	if len(errs) == 0 {
		t.Fatal("expected errors, got none")
	}
}

// TestValidatePodClaims_IndividualSliceUsesCoreID counts coreID devices per claim.
func TestValidatePodClaims_IndividualSliceUsesCoreID(t *testing.T) {
	clientset := fake.NewSimpleClientset(
		resourceClaimWithAllocation("default", "claim-2", []resourceapi.DeviceRequestAllocationResult{
			{Request: "req", Driver: admission.DefaultDriverName, Pool: "pool", Device: "cpudev001"},
			{Request: "req", Driver: admission.DefaultDriverName, Pool: "pool", Device: "cpudev002"},
		}),
		resourceSliceWithCoreIDs([]string{"cpudev001", "cpudev002"}),
	)

	handler := &admissionHandler{
		driverName: admission.DefaultDriverName,
		clientset:  clientset,
	}

	pod := podWithClaims("default", "pod-coreid", "claim-ref", "claim-2")
	pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("2"),
	}

	errs := handler.validatePodClaims(pod)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

// TestCPURequestCount_RoundsFractionalToOne verifies fractional CPU rounds up to 1.
func TestCPURequestCount_RoundsFractionalToOne(t *testing.T) {
	count, specified := cpuRequestCount(corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("500m"),
	})
	if !specified {
		t.Fatal("expected cpu to be specified")
	}
	if count != 1 {
		t.Fatalf("expected count to round to 1, got %d", count)
	}
}

// resourceClaim builds a ResourceClaim object for tests.
func resourceClaim(namespace, name string, req resourceapi.ExactDeviceRequest) *resourceapi.ResourceClaim {
	return &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: resourceapi.ResourceClaimSpec{
			Devices: resourceapi.DeviceClaim{
				Requests: []resourceapi.DeviceRequest{
					{
						Name:    "req",
						Exactly: &req,
					},
				},
			},
		},
	}
}

// resourceClaimWithAllocation builds a ResourceClaim with allocation results.
func resourceClaimWithAllocation(namespace, name string, results []resourceapi.DeviceRequestAllocationResult) *resourceapi.ResourceClaim {
	return &resourceapi.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: resourceapi.ResourceClaimSpec{
			Devices: resourceapi.DeviceClaim{
				Requests: []resourceapi.DeviceRequest{
					{
						Name:    "req",
						Exactly: &resourceapi.ExactDeviceRequest{DeviceClassName: admission.DefaultDriverName, Count: int64(len(results))},
					},
				},
			},
		},
		Status: resourceapi.ResourceClaimStatus{
			Allocation: &resourceapi.AllocationResult{
				Devices: resourceapi.DeviceAllocationResult{Results: results},
			},
		},
	}
}

// exactCountRequest builds a dra.cpu ExactDeviceRequest with a count.
func exactCountRequest(count int64) resourceapi.ExactDeviceRequest {
	return resourceapi.ExactDeviceRequest{
		DeviceClassName: admission.DefaultDriverName,
		Count:           count,
	}
}

// resourceSliceWithCoreIDs builds a ResourceSlice with coreID attributes per device.
func resourceSliceWithCoreIDs(deviceNames []string) *resourceapi.ResourceSlice {
	devices := make([]resourceapi.Device, 0, len(deviceNames))
	for i, name := range deviceNames {
		coreID := int64(i)
		devices = append(devices, resourceapi.Device{
			Name: name,
			Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
				resourceapi.QualifiedName("dra.cpu/coreID"): {IntValue: &coreID},
			},
		})
	}
	return &resourceapi.ResourceSlice{
		ObjectMeta: metav1.ObjectMeta{
			Name: "slice-coreid",
		},
		Spec: resourceapi.ResourceSliceSpec{
			Driver: admission.DefaultDriverName,
			Pool: resourceapi.ResourcePool{
				Name:               "pool",
				Generation:         1,
				ResourceSliceCount: 1,
			},
			NodeName: nil,
			Devices:  devices,
		},
	}
}

// podWithClaims builds a Pod referencing a ResourceClaim by name.
func podWithClaims(namespace, name, claimRefName, claimName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: corev1.PodSpec{
			ResourceClaims: []corev1.PodResourceClaim{
				{
					Name:              claimRefName,
					ResourceClaimName: strPtr(claimName),
				},
			},
			Containers: []corev1.Container{
				{
					Name: "main",
					Resources: corev1.ResourceRequirements{
						Claims: []corev1.ResourceClaim{
							{Name: claimRefName},
						},
					},
				},
			},
		},
	}
}

// strPtr returns a string pointer for convenience in tests.
func strPtr(value string) *string {
	return &value
}
