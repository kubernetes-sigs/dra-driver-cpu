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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fakeClaimCPUCountGetter returns CPU counts from a map keyed by "namespace/name".
type fakeClaimCPUCountGetter map[string]int64

func (f fakeClaimCPUCountGetter) ClaimCPUCount(_ context.Context, namespace, claimName string) (int64, error) {
	if v, ok := f[namespace+"/"+claimName]; ok {
		return v, nil
	}
	return 0, nil
}

// fakeGetterAllocated returns ErrClaimAlreadyAllocated for claims in the allocated set (namespace/name).
type fakeGetterAllocated struct {
	allocated map[string]bool
	counts    map[string]int64
}

func (f fakeGetterAllocated) ClaimCPUCount(_ context.Context, namespace, claimName string) (int64, error) {
	if f.allocated[namespace+"/"+claimName] {
		return 0, ErrClaimAlreadyAllocated
	}
	if v, ok := f.counts[namespace+"/"+claimName]; ok {
		return v, nil
	}
	return 0, nil
}

func TestValidatePodClaims_ClaimAlreadyAllocatedRejected(t *testing.T) {
	getter := fakeGetterAllocated{
		allocated: map[string]bool{"default/claim-4": true},
		counts:    map[string]int64{},
	}
	pod := podWithClaims("default", "pod-allocated", "claim-ref", "claim-4")
	pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("4"),
	}

	errs := ValidatePodClaims(context.Background(), pod, DefaultDriverName, getter)
	if len(errs) == 0 {
		t.Fatal("expected error (claim already allocated), got none")
	}
	if len(errs) != 1 || !strings.Contains(errs[0], "already allocated") {
		t.Fatalf("expected 'already allocated' error, got %v", errs)
	}
}

func TestValidatePodClaims_CPURequestMatchesClaimCount(t *testing.T) {
	getter := fakeClaimCPUCountGetter{"default/claim-4": 4}
	pod := podWithClaims("default", "pod-ok", "claim-ref", "claim-4")
	pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("4"),
	}

	errs := ValidatePodClaims(context.Background(), pod, DefaultDriverName, getter)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

// With pod-level validation, a pod with dra.cpu claims must have total CPU request equal to claim total.
func TestValidatePodClaims_NoCPURequestWithClaimRejected(t *testing.T) {
	getter := fakeClaimCPUCountGetter{"default/claim-2": 2}
	pod := podWithClaims("default", "pod-claim-only", "claim-ref", "claim-2")

	errs := ValidatePodClaims(context.Background(), pod, DefaultDriverName, getter)
	if len(errs) == 0 {
		t.Fatal("expected error (pod CPU requests 0 < claim total 2), got none")
	}
}

func TestValidatePodClaims_MissingClaimDoesNotFail(t *testing.T) {
	getter := fakeClaimCPUCountGetter{}
	pod := podWithClaims("default", "pod-missing-claim", "claim-ref", "claim-does-not-exist")
	pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("2"),
	}

	errs := ValidatePodClaims(context.Background(), pod, DefaultDriverName, getter)
	if len(errs) != 0 {
		t.Fatalf("expected no errors (missing claim returns 0), got %v", errs)
	}
}

func TestValidatePodClaims_NoCPUAndNoClaimSkipsValidation(t *testing.T) {
	getter := fakeClaimCPUCountGetter{}
	pod := &corev1.Pod{} //nolint:exhaustruct

	errs := ValidatePodClaims(context.Background(), pod, DefaultDriverName, getter)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestValidatePodClaims_CPUMismatchRejected(t *testing.T) {
	getter := fakeClaimCPUCountGetter{"default/claim-4": 4}
	pod := podWithClaims("default", "pod-mismatch", "claim-ref", "claim-4")
	pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("2"),
	}

	errs := ValidatePodClaims(context.Background(), pod, DefaultDriverName, getter)
	if len(errs) == 0 {
		t.Fatal("expected errors, got none")
	}
}

func TestValidatePodClaims_PodCPUExceedsClaimTotalRejected(t *testing.T) {
	getter := fakeClaimCPUCountGetter{"default/claim-4": 4}
	pod := podWithClaims("default", "pod-excess", "claim-ref", "claim-4")
	pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("6"),
	}

	errs := ValidatePodClaims(context.Background(), pod, DefaultDriverName, getter)
	if len(errs) == 0 {
		t.Fatal("expected error (pod CPU 6 != claim total 4), got none")
	}
}

func TestValidatePodClaims_CPUQuantityMustBeInteger(t *testing.T) {
	getter := fakeClaimCPUCountGetter{"default/claim-2": 2}
	pod := podWithClaims("default", "pod-fractional", "claim-ref", "claim-2")
	pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("500m"),
	}

	errs := ValidatePodClaims(context.Background(), pod, DefaultDriverName, getter)
	if len(errs) == 0 {
		t.Fatal("expected errors, got none")
	}
}

func TestValidatePodClaims_IndividualSliceUsesCoreID(t *testing.T) {
	getter := fakeClaimCPUCountGetter{"default/claim-2": 2}
	pod := podWithClaims("default", "pod-coreid", "claim-ref", "claim-2")
	pod.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
		corev1.ResourceCPU: resource.MustParse("2"),
	}

	errs := ValidatePodClaims(context.Background(), pod, DefaultDriverName, getter)
	if len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestCPURequestCount_RoundsFractionalToOne(t *testing.T) {
	for _, tt := range []struct {
		qty  string
		want int64
	}{
		{"400m", 1},
		{"500m", 1},
	} {
		count := CPURequestCount(resource.MustParse(tt.qty))
		if count != tt.want {
			t.Fatalf("%s: expected %d, got %d", tt.qty, tt.want, count)
		}
	}
}

func podWithClaims(namespace, name, claimRefName, claimName string) *corev1.Pod {
	return &corev1.Pod{ //nolint:exhaustruct
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

func strPtr(s string) *string { return &s }
