/*
Copyright The Kubernetes Authors.

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

package driver

import (
	"fmt"

	"github.com/containerd/nri/pkg/api"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/cpuset"
)

const (
	// minCPUShares is the Kubernetes minimum CPU shares for best-effort containers.
	// Kubernetes sets this value for containers without CPU requests.
	minCPUShares = uint64(2)

	// cpuRequestTolerance allows for minor floating-point precision differences
	// when comparing CPU requests (in cores) to claim allocations (in integer CPUs).
	// Set to 0.01 to allow ~10ms difference per CPU core.
	cpuRequestTolerance = 0.01
)

// ClaimAllocations represents CPU allocations from DRA claims.
type ClaimAllocations map[types.UID]cpuset.CPUSet

// TotalCPUs returns the total number of CPUs across all claims.
func (ca ClaimAllocations) TotalCPUs() int {
	total := 0
	for _, cpus := range ca {
		total += cpus.Size()
	}
	return total
}

// Validate checks that container CPU requests match the claim allocations.
//
// Validation behavior:
//   - When container CPU shares > minCPUShares (2): validates that the request matches claim allocation
//   - When container CPU shares <= minCPUShares or not set: validation passes (allows best-effort containers
//     and Pod Level Resources scenarios where container-level requests are not specified)
//
// This lightweight validation serves as a final defense against common misconfigurations.
func (ca ClaimAllocations) Validate(pod *api.PodSandbox, ctr *api.Container) error {
	totalClaimCPUs := ca.TotalCPUs()

	if ctr.Linux != nil && ctr.Linux.Resources != nil && ctr.Linux.Resources.Cpu != nil {
		if ctr.Linux.Resources.Cpu.Shares != nil && ctr.Linux.Resources.Cpu.Shares.Value > minCPUShares {
			containerCPUShares := ctr.Linux.Resources.Cpu.Shares.Value
			containerCPURequest := float64(containerCPUShares) / 1024.0

			diff := containerCPURequest - float64(totalClaimCPUs)
			if diff < -cpuRequestTolerance || diff > cpuRequestTolerance {
				return fmt.Errorf(
					"container %s CPU request (%.2f cores, %d shares) does not match claim allocation (%d CPUs): "+
						"when using DRA CPU claims, container CPU requests must exactly equal the claim size",
					ctr.Name, containerCPURequest, containerCPUShares, totalClaimCPUs,
				)
			}
		}
	}

	return nil
}
