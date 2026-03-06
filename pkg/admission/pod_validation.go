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
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// ErrClaimAlreadyAllocated is returned by ClaimCPUCountGetter when the ResourceClaim is already allocated.
var ErrClaimAlreadyAllocated = errors.New("resourceclaim already allocated")

// ClaimCPUCountGetter returns the total CPU count for a ResourceClaim by name.
// Used by ValidatePodClaims to resolve claim references without depending on a Kubernetes client.
type ClaimCPUCountGetter interface {
	ClaimCPUCount(ctx context.Context, namespace, claimName string) (int64, error)
}

// ValidatePodClaims enforces at pod level: when a pod has dra.cpu claims, the sum of
// non-init container CPU requests must equal the sum of CPUs from those claims.
// It returns a list of errors
func ValidatePodClaims(ctx context.Context, pod *corev1.Pod, driverName string, getter ClaimCPUCountGetter) []string {
	if pod == nil || len(pod.Spec.ResourceClaims) == 0 {
		return nil
	}

	claimNameToResource := make(map[string]string)
	for _, rc := range pod.Spec.ResourceClaims {
		if rc.Name == "" || rc.ResourceClaimName == nil {
			continue
		}
		claimNameToResource[rc.Name] = *rc.ResourceClaimName
	}

	if len(claimNameToResource) == 0 {
		return nil
	}

	var totalPodCPU int64
	var totalClaimCPUs int64
	var errs []string

	for _, container := range pod.Spec.Containers {
		cpuQuantity, hasCPU := container.Resources.Requests[corev1.ResourceCPU]
		if hasCPU {
			totalPodCPU += CPURequestCount(cpuQuantity)
		}
		for _, claim := range container.Resources.Claims {
			resourceClaimName, ok := claimNameToResource[claim.Name]
			if !ok {
				continue
			}
			claimCPUs, err := getter.ClaimCPUCount(ctx, pod.Namespace, resourceClaimName)
			if err != nil {
				if errors.Is(err, ErrClaimAlreadyAllocated) {
					errs = append(errs, fmt.Sprintf("ResourceClaim %q is already allocated", resourceClaimName))
				} else {
					errs = append(errs, fmt.Sprintf("failed to get ResourceClaim %q: %v", resourceClaimName, err))
				}
				continue
			}
			totalClaimCPUs += claimCPUs
		}
	}

	if totalClaimCPUs > 0 && totalPodCPU != totalClaimCPUs {
		errs = append(errs, fmt.Sprintf("pod CPU requests (%d) must match dra.cpu claim total (%d)", totalPodCPU, totalClaimCPUs))
	}

	return errs
}

// CPURequestCount returns the CPU quantity as integer cores, rounding up to the nearest integer.
// Returns 0 when the quantity is <= 0.
func CPURequestCount(cpuQuantity resource.Quantity) int64 {
	millis := cpuQuantity.MilliValue()
	if millis <= 0 {
		return 0
	}
	value := cpuQuantity.Value()
	if value*1000 == millis {
		return value
	}
	// Round up to whole cores: 400m -> 1, 500m -> 1, 1500m -> 2
	return (millis + 999) / 1000
}
