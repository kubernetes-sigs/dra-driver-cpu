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
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/discovery"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/node"
	e2epod "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/pod"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/cpuset"
)

const (
	maxExclusivePods = 10
	cpusPerClaim     = 2
	// should be at least cpusPerClaim + 1 to leave some CPUs for the shared pool
	minCPUsAvailableForPodAllocation = 3
)

/*
gingko flags explained:

- Serial:
because the tests want to change the CPU allocation, which is a giant blob of node shared state.
- Ordered:
to do the relatively costly initial resource discovery on the target node only once
- ContinueOnFailure
to mitigate the problem that ordered suites stop on the first failure, so an initial failure can mask
a cascade of latter failure; this makes the tests failure troubleshooting painful, as we would need
to fix failures one by one vs in batches.

Note that using "Ordered" may introduce subtle bugs caused by incorrect tests which pollute or leak
state. We should keep looking for ways to eventually remove "Ordered".
Please note "Serial" is however unavoidable because we manage the shared node state.
*/
var _ = ginkgo.Describe("CPU Allocation", ginkgo.Serial, ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		rootFxt             *fixture.Fixture
		targetNode          *v1.Node
		targetNodeCPUInfo   discovery.DRACPUInfo
		availableCPUs       cpuset.CPUSet
		availableCPUsByNode map[string]cpuset.CPUSet
		dracpuTesterImage   string
		reservedCPUs        cpuset.CPUSet
		cpuDeviceMode       string
	)

	ginkgo.BeforeAll(func(ctx context.Context) {
		// early cheap check before to create the Fixture, so we use GinkgoLogr directly
		dracpuTesterImage = os.Getenv("DRACPU_E2E_TEST_IMAGE")
		gomega.Expect(dracpuTesterImage).ToNot(gomega.BeEmpty(), "missing environment variable DRACPU_E2E_TEST_IMAGE")
		ginkgo.GinkgoLogr.Info("discovery image", "pullSpec", dracpuTesterImage)

		var err error
		if reservedCPUVal := os.Getenv("DRACPU_E2E_RESERVED_CPUS"); len(reservedCPUVal) > 0 {
			reservedCPUs, err = cpuset.Parse(reservedCPUVal)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			ginkgo.GinkgoLogr.Info("reserved CPUs", "value", reservedCPUs.String())
		}

		rootFxt, err = fixture.ForGinkgo()
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create root fixture: %v", err)
		infraFxt := rootFxt.WithPrefix("infra")
		gomega.Expect(infraFxt.Setup(ctx)).To(gomega.Succeed())
		ginkgo.DeferCleanup(infraFxt.Teardown)

		ginkgo.By("checking the daemonset configuration matches the test configuration")
		daemonSet, err := rootFxt.K8SClientset.AppsV1().DaemonSets("kube-system").Get(ctx, "dracpu", metav1.GetOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get dracpu daemonset")
		gomega.Expect(daemonSet.Spec.Template.Spec.Containers).ToNot(gomega.BeEmpty(), "no containers in dracpu daemonset")
		var dsReservedCPUs cpuset.CPUSet
		for _, arg := range daemonSet.Spec.Template.Spec.Containers[0].Args {
			if val, ok := parseReservedCPUsArg(arg); ok {
				dsReservedCPUs, err = cpuset.Parse(val)
				gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot parse daemonset reserved cpus: %v", err)
			}
			if val, ok := parseCPUDeviceModeArg(arg); ok {
				cpuDeviceMode = val
			}
		}
		rootFxt.Log.Info("daemonset --reserved-cpus configuration", "cpus", dsReservedCPUs.String())
		gomega.Expect(dsReservedCPUs.Equals(reservedCPUs)).To(gomega.BeTrue(), "daemonset reserved cpus %v do not match test reserved cpus %v", dsReservedCPUs.String(), reservedCPUs.String())
		rootFxt.Log.Info("daemonset --cpu-device-mode configuration", "mode", cpuDeviceMode)

		var workerNodes []*v1.Node
		if targetNodeName := os.Getenv("DRACPU_E2E_TARGET_NODE"); len(targetNodeName) > 0 {
			targetNode, err = rootFxt.K8SClientset.CoreV1().Nodes().Get(ctx, targetNodeName, metav1.GetOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get worker node %q: %v", targetNodeName, err)
			workerNodes = []*v1.Node{targetNode}
		} else {
			gomega.Eventually(func() error {
				var findErr error
				workerNodes, findErr = node.FindWorkers(ctx, infraFxt.K8SClientset)
				if findErr != nil {
					return findErr
				}
				if len(workerNodes) == 0 {
					return fmt.Errorf("no worker nodes detected")
				}
				targetNode = workerNodes[0]
				return nil
			}).WithTimeout(1*time.Minute).WithPolling(5*time.Second).Should(gomega.Succeed(), "failed to find any worker node")
		}
		rootFxt.Log.Info("using worker node", "nodeName", targetNode.Name)

		availableCPUsByNode = make(map[string]cpuset.CPUSet)
		for _, n := range workerNodes {
			infoPod := discovery.MakePod(infraFxt.Namespace.Name, dracpuTesterImage)
			infoPod.Name = "discovery-pod-" + n.Name
			infoPod = e2epod.PinToNode(infoPod, n.Name)
			infoPod, err = e2epod.RunToCompletion(ctx, infraFxt.K8SClientset, infoPod)
			gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create discovery pod on node %q: %v", n.Name, err)
			data, err := e2epod.GetLogs(infraFxt.K8SClientset, ctx, infoPod.Namespace, infoPod.Name, infoPod.Spec.Containers[0].Name)
			gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get logs from discovery pod: %v", err)
			var nodeCPUInfo discovery.DRACPUInfo
			gomega.Expect(json.Unmarshal([]byte(data), &nodeCPUInfo)).To(gomega.Succeed())
			if n.Name == targetNode.Name {
				targetNodeCPUInfo = nodeCPUInfo
			}
			allocatable := makeCPUSetFromDiscoveredCPUInfo(nodeCPUInfo)
			available := allocatable.Difference(reservedCPUs)
			if reservedCPUs.Size() > 0 {
				gomega.Expect(available.Intersection(reservedCPUs).Size()).To(gomega.BeZero(), "node %q: available cpus %v overlap reserved %v", n.Name, available.String(), reservedCPUs.String())
			}
			availableCPUsByNode[n.Name] = available
			rootFxt.Log.Info("checking worker node", "nodeName", n.Name, "coreCount", len(nodeCPUInfo.CPUs), "allocatableCPUs", allocatable.String(), "availableCPUs", available.String())
		}
		availableCPUs = availableCPUsByNode[targetNode.Name]
	})

	ginkgo.When("setting resource claims", func() {
		var fxt *fixture.Fixture

		ginkgo.BeforeEach(func(ctx context.Context) {
			fxt = rootFxt.WithPrefix("with-claims")
			gomega.Expect(fxt.Setup(ctx)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func(ctx context.Context) {
			gomega.Expect(fxt.Teardown(ctx)).To(gomega.Succeed())
		})

		ginkgo.Context("for exclusive CPU allocation", func() {
			// TODO: check and ensure cpumanager configuration?

			ginkgo.JustBeforeEach(func(ctx context.Context) {
				fixture.By("checking the target nodes has at least %d allocatable cpus", minCPUsAvailableForPodAllocation)
				if availableCPUs.Size() < minCPUsAvailableForPodAllocation {
					ginkgo.Skip(fmt.Sprintf("exclusive allocation tests require at least %d cpus in the worker node", minCPUsAvailableForPodAllocation))
				}
				fixture.By("found target nodes with %d allocatable cpus", len(targetNodeCPUInfo.CPUs))
			})

			ginkgo.It("should allocate exclusive CPUs and remove from the shared pool", func(ctx context.Context) {
				fixture.By("creating a best-effort reference pod")
				shrPod1 := mustCreateBestEffortPod(ctx, fxt, targetNode.Name, dracpuTesterImage)

				fixture.By("checking the best-effort reference pod %s has access to all the non-reserved node CPUs through the shared pool", e2epod.Identify(shrPod1))
				sharedAllocPre := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, shrPod1)
				fxt.Log.Info("checking shared allocation", "pod", e2epod.Identify(shrPod1), "cpuAllocated", sharedAllocPre.CPUAssigned.String(), "cpuAffinity", sharedAllocPre.CPUAffinity.String())

				numCPUs := availableCPUs.Size()
				// Ensure at least 1 CPU is available for the shared pool
				maxExclusiveCpus := numCPUs - 1
				if maxExclusiveCpus < 0 {
					maxExclusiveCpus = 0
				}
				numPods := maxExclusiveCpus / cpusPerClaim
				if numPods > maxExclusivePods {
					numPods = maxExclusivePods
				}

				fxt.Log.Info("Creating pods requesting exclusive CPUs", "numPods", numPods, "cpusPerClaim", cpusPerClaim)
				var exclPods []*v1.Pod
				// CPU IDs are per-node; track allocations per node so we don't treat same IDs on different nodes as overlapping.
				allAllocatedCPUsByNode := make(map[string]cpuset.CPUSet)

				claimTemplate := resourcev1.ResourceClaimTemplate{
					ObjectMeta: metav1.ObjectMeta{
						Name: fmt.Sprintf("cpu-request-%d-excl", cpusPerClaim),
					},
					Spec: resourcev1.ResourceClaimTemplateSpec{
						Spec: makeResourceClaimSpec(cpusPerClaim, cpuDeviceMode == "grouped"),
					},
				}
				createdClaimTemplate, err := fxt.K8SClientset.ResourceV1().ResourceClaimTemplates(fxt.Namespace.Name).Create(ctx, &claimTemplate, metav1.CreateOptions{})
				for i := 0; i < numPods; i++ {
					gomega.Expect(err).ToNot(gomega.HaveOccurred())
					pod := makeTesterPodWithExclusiveCPUClaim(fxt.Namespace.Name, dracpuTesterImage, createdClaimTemplate.Name, int64(cpusPerClaim))
					createdPod, err := e2epod.CreateSync(ctx, fxt.K8SClientset, pod)
					gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create tester pod %d: %v", i, err)
					exclPods = append(exclPods, createdPod)
				}

				fixture.By("Verifying CPU allocations for each exclusive pod")
				for i, pod := range exclPods {
					var alloc CPUAllocation
					nodeName := pod.Spec.NodeName
					gomega.Eventually(func() error {
						alloc = getTesterPodCPUAllocation(fxt.K8SClientset, ctx, pod)
						if alloc.CPUAssigned.Size() != cpusPerClaim {
							return fmt.Errorf("pod %d: got %d CPUs, want %d", i, alloc.CPUAssigned.Size(), cpusPerClaim)
						}
						availableOnNode := availableCPUsByNode[nodeName]
						if availableOnNode.Size() == 0 {
							availableOnNode = availableCPUs
						}
						if !alloc.CPUAssigned.IsSubsetOf(availableOnNode) {
							return fmt.Errorf("pod %d on node %s: CPUs %s outside node available set %s", i, nodeName, alloc.CPUAssigned.String(), availableOnNode.String())
						}
						allocatedOnNode := allAllocatedCPUsByNode[nodeName]
						if allocatedOnNode.Intersection(alloc.CPUAssigned).Size() != 0 {
							return fmt.Errorf("pod %d: overlapping CPUs with previous pods on node %s", i, nodeName)
						}
						return nil
					}).WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(gomega.Succeed(), "exclusive pod %d allocation did not stabilize", i)
					fxt.Log.Info("Checking exclusive CPU allocation", "pod", e2epod.Identify(pod), "cpuAllocated", alloc.CPUAssigned.String())
					allAllocatedCPUsByNode[nodeName] = allAllocatedCPUsByNode[nodeName].Union(alloc.CPUAssigned)
				}
				var totalAllocated int
				for _, cpus := range allAllocatedCPUsByNode {
					totalAllocated += cpus.Size()
				}
				gomega.Expect(totalAllocated).To(gomega.Equal(numPods * cpusPerClaim))
				rootFxt.Log.Info("All exclusive allocation", "byNode", allAllocatedCPUsByNode, "expected Shared CPUs on target", availableCPUs.Difference(allAllocatedCPUsByNode[targetNode.Name]).String())

				fixture.By("checking the shared pool does not include anymore the exclusively allocated CPUs on each node with exclusive pods")
				var shrPod2 *v1.Pod
				var otherNodeSharedPods []*v1.Pod
				for nodeName := range allAllocatedCPUsByNode {
					availableOnNode := availableCPUsByNode[nodeName]
					if availableOnNode.Size() == 0 {
						availableOnNode = availableCPUs
					}
					expectedSharedOnNode := availableOnNode.Difference(allAllocatedCPUsByNode[nodeName])
					if nodeName == targetNode.Name {
						fixture.By("creating a second best-effort reference pod on target node")
						shrPod2 = mustCreateBestEffortPod(ctx, fxt, targetNode.Name, dracpuTesterImage)
						verifySharedPoolMatches(ctx, fxt, shrPod2, expectedSharedOnNode)
						ginkgo.By("checking the CPU pool of the best-effort pod created before the pods with CPU resource claims")
						verifySharedPoolMatches(ctx, fxt, shrPod1, expectedSharedOnNode)
						continue
					}
					fixture.By("creating best-effort pod on node %s to verify shared pool", nodeName)
					shrPodOther := mustCreateBestEffortPod(ctx, fxt, nodeName, dracpuTesterImage)
					verifySharedPoolMatches(ctx, fxt, shrPodOther, expectedSharedOnNode)
					otherNodeSharedPods = append(otherNodeSharedPods, shrPodOther)
				}

				fixture.By("deleting the pods with exclusive CPUs")
				for _, pod := range exclPods {
					gomega.Expect(e2epod.DeleteSync(ctx, fxt.K8SClientset, pod)).To(gomega.Succeed(), "cannot delete pod %s", e2epod.Identify(pod))
				}

				verifySharedPoolMatches(ctx, fxt, shrPod1, availableCPUs)
				if shrPod2 != nil {
					verifySharedPoolMatches(ctx, fxt, shrPod2, availableCPUs)
				}
				for _, shrPodOther := range otherNodeSharedPods {
					nodeName := shrPodOther.Spec.NodeName
					availableOnNode := availableCPUsByNode[nodeName]
					if availableOnNode.Size() == 0 {
						availableOnNode = availableCPUs
					}
					verifySharedPoolMatches(ctx, fxt, shrPodOther, availableOnNode)
					gomega.Expect(e2epod.DeleteSync(ctx, fxt.K8SClientset, shrPodOther)).To(gomega.Succeed(), "cannot delete helper pod %s", e2epod.Identify(shrPodOther))
				}
			})
		})
	})
})

func verifySharedPoolMatches(ctx context.Context, fxt *fixture.Fixture, sharedPod *v1.Pod, expectedSharedCPUs cpuset.CPUSet) {
	ginkgo.GinkgoHelper()

	fixture.By("checking the CPU pool of the best-effort tester pod %s matches expected %s", e2epod.Identify(sharedPod), expectedSharedCPUs.String())
	gomega.Eventually(func() error {
		sharedAllocUpdated := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, sharedPod)
		fxt.Log.Info("checking shared allocation", "pod", e2epod.Identify(sharedPod), "cpuAllocated", sharedAllocUpdated.CPUAssigned.String(), "cpuAffinity", sharedAllocUpdated.CPUAffinity.String())
		if !expectedSharedCPUs.Equals(sharedAllocUpdated.CPUAssigned) {
			return fmt.Errorf("shared CPUs mismatch: expected %v got %v", expectedSharedCPUs.String(), sharedAllocUpdated.CPUAssigned.String())
		}
		return nil
	}).WithTimeout(2*time.Minute).WithPolling(5*time.Second).Should(gomega.Succeed(), "the best-effort tester pod %s CPU pool did not match expected %s", e2epod.Identify(sharedPod), expectedSharedCPUs.String())
}
