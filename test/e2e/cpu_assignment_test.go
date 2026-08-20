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
	"fmt"
	"os"
	"time"

	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/discovery"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	cpusetmatchers "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/matchers/cpuset"
	resourceclaimmatchers "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/matchers/resourceclaim"
	e2enode "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/node"
	e2epod "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/pod"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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
		rootFxt                       *fixture.Fixture
		targetNode                    *v1.Node
		targetNodeCPUInfo             discovery.DRACPUInfo
		availableCPUs                 cpuset.CPUSet
		dracpuTesterImage             string
		reservedCPUs                  cpuset.CPUSet
		cpuDeviceMode                 string
		groupBy                       string
		publishNodeAllocatableMapping bool
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
		cfgValues, err := getDriverConfigValues(ctx, rootFxt.K8SClientset, "kube-system", daemonSet)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot read dracpu driver config values")
		var dsReservedCPUs cpuset.CPUSet
		if len(cfgValues.ReservedCPUs) > 0 {
			dsReservedCPUs, err = cpuset.Parse(cfgValues.ReservedCPUs)
			gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot parse daemonset reserved cpus: %v", err)
		}
		cpuDeviceMode = cfgValues.CPUDeviceMode
		groupBy = cfgValues.GroupBy
		rootFxt.Log.Info("daemonset --reserved-cpus configuration", "cpus", dsReservedCPUs.String())
		gomega.Expect(dsReservedCPUs).To(cpusetmatchers.Equal(reservedCPUs), "daemonset reserved cpus do not match test reserved cpus")
		rootFxt.Log.Info("daemonset --cpu-device-mode configuration", "mode", cpuDeviceMode, "groupBy", groupBy)
		driverConfig := getDriverConfig(ctx, rootFxt.K8SClientset)
		publishNodeAllocatableMapping = driverConfig.PublishNodeAllocatableResourceMapping
		rootFxt.Log.Info("driver node allocatable mapping", "enabled", publishNodeAllocatableMapping)

		targetNode, err = e2enode.PickWorker(ctx, rootFxt.K8SClientset, 5*time.Second, 1*time.Minute, rootFxt.Log)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		rootFxt.Log.Info("using worker node", "nodeName", targetNode.Name)

		infoPod := discovery.MakePod(infraFxt.Namespace.Name, dracpuTesterImage)
		infoPod = e2epod.PinToNode(infoPod, targetNode.Name)
		infoPod, err = e2epod.RunToCompletion(ctx, infraFxt.K8SClientset, infoPod)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create discovery pod: %v", err)
		data, err := e2epod.GetLogs(ctx, infraFxt.K8SClientset, infoPod)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get logs from discovery pod: %v", err)
		gomega.Expect(unmarshalLatestReport(data, &targetNodeCPUInfo)).To(gomega.Succeed())

		allocatableCPUs := makeCPUSetFromDiscoveredCPUInfo(targetNodeCPUInfo)
		availableCPUs = allocatableCPUs.Difference(reservedCPUs)
		if reservedCPUs.Size() > 0 {
			gomega.Expect(availableCPUs).To(cpusetmatchers.HaveNoOverlapWith(reservedCPUs))
		}
		rootFxt.Log.Info("checking worker node", "nodeName", infoPod.Spec.NodeName, "coreCount", len(targetNodeCPUInfo.CPUs), "allocatableCPUs", allocatableCPUs.String(), "reservedCPUs", reservedCPUs.String(), "availableCPUs", availableCPUs.String())
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

		ginkgo.It("should fail to create a container with malformed DRA_CPUSET env", ginkgo.Label("negative"), func(ctx context.Context) {
			pod := makeTesterPodBestEffort(fxt.Namespace.Name, dracpuTesterImage)
			pod.GenerateName = "tester-pod-malformed-dra-cpuset-"
			pod.Spec.RestartPolicy = v1.RestartPolicyNever
			pod.Spec.Containers[0].Env = []v1.EnvVar{
				{Name: "DRA_CPUSET_malformed_claim", Value: "a-b"},
			}
			pod = e2epod.PinToNode(pod, targetNode.Name)

			createdPod, err := fxt.K8SClientset.CoreV1().Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			EventuallyFailedToCreate(ctx, fxt, createdPod)
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
				if cpuDeviceMode == "grouped" && groupBy == "machine" {
					ginkgo.Skip("skipping this test in machine grouping mode as we do not configure opaque config in claim")
				}
				fixture.By("creating a best-effort reference pod")
				shrPod1 := mustCreateBestEffortPod(ctx, fxt, targetNode.Name, dracpuTesterImage)

				fixture.By("checking the best-effort reference pod %s has access to all the non-reserved node CPUs through the shared pool", e2epod.Identify(shrPod1))
				sharedAllocPre := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, shrPod1)
				fxt.Log.Info("checking shared allocation", "pod", e2epod.Identify(shrPod1), "cpuAllocated", sharedAllocPre.CPUAssigned.String(), "cpuAffinity", sharedAllocPre.CPUAffinity.String())

				numCPUs := availableCPUs.Size()
				// Ensure at least 1 CPU is available for the shared pool
				maxExclusiveCpus := max(numCPUs-1, 0)
				numPods := min(maxExclusiveCpus/cpusPerClaim, maxExclusivePods)

				fxt.Log.Info("Creating pods requesting exclusive CPUs", "numPods", numPods, "cpusPerClaim", cpusPerClaim)
				var exclPods []*v1.Pod
				allAllocatedCPUs := cpuset.New()

				claimTemplate := resourcev1.ResourceClaimTemplate{
					ObjectMeta: metav1.ObjectMeta{
						Name: fmt.Sprintf("cpu-request-%d-excl", cpusPerClaim),
					},
					Spec: resourcev1.ResourceClaimTemplateSpec{
						Spec: makeResourceClaimSpec(cpusPerClaim, cpuDeviceMode == "grouped"),
					},
				}
				createdClaimTemplate, err := fxt.K8SClientset.ResourceV1().ResourceClaimTemplates(fxt.Namespace.Name).Create(ctx, &claimTemplate, metav1.CreateOptions{})
				for i := range numPods {
					gomega.Expect(err).ToNot(gomega.HaveOccurred())
					pod := makeTesterPodWithExclusiveCPUClaim(fxt.Namespace.Name, dracpuTesterImage, createdClaimTemplate.Name, int64(cpusPerClaim), targetNode.Name, publishNodeAllocatableMapping)
					createdPod, err := e2epod.CreateSync(ctx, fxt.K8SClientset, pod)
					gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create tester pod %d: %v", i, err)
					exclPods = append(exclPods, createdPod)
				}

				fixture.By("Verifying CPU allocations for each exclusive pod")
				for i, pod := range exclPods {
					alloc := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, pod)
					fxt.Log.Info("Checking exclusive CPU allocation", "pod", e2epod.Identify(pod), "cpuAllocated", alloc.CPUAssigned.String())
					gomega.Expect(alloc.CPUAssigned).To(cpusetmatchers.HaveSize(cpusPerClaim), "Pod %d did not get %d CPUs", i, cpusPerClaim)
					gomega.Expect(alloc.CPUAssigned).To(cpusetmatchers.BeSubsetOf(availableCPUs), "Pod %d got CPUs outside available set", i)
					gomega.Expect(alloc.CPUAssigned).To(cpusetmatchers.HaveNoOverlapWith(allAllocatedCPUs), "Pod %d has overlapping CPUs", i)
					allAllocatedCPUs = allAllocatedCPUs.Union(alloc.CPUAssigned)
					// The scheduler reports the claim's CPUs in the pod status when the
					// driver publishes node allocatable mappings; the field must be absent
					// otherwise.
					expectNodeAllocatableClaimStatus(ctx, fxt.K8SClientset, pod, int64(cpusPerClaim), publishNodeAllocatableMapping)
				}
				gomega.Expect(allAllocatedCPUs).To(cpusetmatchers.HaveSize(numPods * cpusPerClaim))
				rootFxt.Log.Info("All exclusive allocation", "pod", "exclusive CPUs", allAllocatedCPUs.String(), "expected Shared CPUs", availableCPUs.Difference(allAllocatedCPUs).String())

				fixture.By("checking the shared pool does not include anymore the exclusively allocated CPUs")
				expectedSharedCPUs := availableCPUs.Difference(allAllocatedCPUs)

				fixture.By("creating a second best-effort reference pod")
				shrPod2 := mustCreateBestEffortPod(ctx, fxt, targetNode.Name, dracpuTesterImage)
				gomega.Eventually(observeAssignedCPUs(ctx, fxt, shrPod2)).WithTimeout(1*time.Minute).WithPolling(5*time.Second).Should(cpusetmatchers.Equal(expectedSharedCPUs), "the best-effort tester pod %s does not have access to the exclusively allocated CPUs", e2epod.Identify(shrPod2))

				ginkgo.By("checking the CPU pool of the best-effort pod created before the pods with CPU resource claims")
				gomega.Eventually(observeAssignedCPUs(ctx, fxt, shrPod1)).WithTimeout(1*time.Minute).WithPolling(5*time.Second).Should(cpusetmatchers.Equal(expectedSharedCPUs), "the best-effort tester pod %s does not have access to the exclusively allocated CPUs", e2epod.Identify(shrPod1))

				fixture.By("deleting the pods with exclusive CPUs")
				for _, pod := range exclPods {
					gomega.Expect(e2epod.DeleteSync(ctx, fxt.K8SClientset, pod)).To(gomega.Succeed(), "cannot delete pod %s", e2epod.Identify(pod))
				}

				ginkgo.By("checking existing shared containers keep their last cpuset until the next CreateContainer or Synchronize")
				gomega.Eventually(observeAssignedCPUs(ctx, fxt, shrPod1)).WithTimeout(1*time.Minute).WithPolling(5*time.Second).Should(cpusetmatchers.Equal(expectedSharedCPUs), "the best-effort tester pod %s does not have access to the exclusively allocated CPUs", e2epod.Identify(shrPod1))
				gomega.Eventually(observeAssignedCPUs(ctx, fxt, shrPod2)).WithTimeout(1*time.Minute).WithPolling(5*time.Second).Should(cpusetmatchers.Equal(expectedSharedCPUs), "the best-effort tester pod %s does not have access to the exclusively allocated CPUs", e2epod.Identify(shrPod2))
			})

			ginkgo.It("should allocate non-overlapping CPUs for multiple requests in the same grouped claim", func(ctx context.Context) {
				if cpuDeviceMode != "grouped" {
					ginkgo.Skip("this test only applies to grouped CPU device mode")
				}
				if groupBy == "machine" {
					ginkgo.Skip("skipping this test in machine grouping mode as we do not configure opaque config in claim")
				}
				desiredTotalCPUs := 2
				if availableCPUs.Size() < desiredTotalCPUs {
					ginkgo.Skip("need at least 2 available CPUs for this test")
				}

				fixture.By("creating a ResourceClaim with two requests each asking for 1 CPU")
				cpuClaim := &resourcev1.ResourceClaim{
					ObjectMeta: metav1.ObjectMeta{
						Namespace:    fxt.Namespace.Name,
						GenerateName: "claim-multi-request-",
					},
					Spec: resourcev1.ResourceClaimSpec{
						Devices: resourcev1.DeviceClaim{
							Requests: []resourcev1.DeviceRequest{
								{
									Name: "request-0",
									Exactly: &resourcev1.ExactDeviceRequest{
										DeviceClassName: "dra.cpu",
										Capacity: &resourcev1.CapacityRequirements{
											Requests: map[resourcev1.QualifiedName]resource.Quantity{
												"dra.cpu/cpu": *resource.NewQuantity(1, resource.DecimalSI),
											},
										},
									},
								},
								{
									Name: "request-1",
									Exactly: &resourcev1.ExactDeviceRequest{
										DeviceClassName: "dra.cpu",
										Capacity: &resourcev1.CapacityRequirements{
											Requests: map[resourcev1.QualifiedName]resource.Quantity{
												"dra.cpu/cpu": *resource.NewQuantity(1, resource.DecimalSI),
											},
										},
									},
								},
							},
						},
					},
				}
				createdClaim, err := fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Create(ctx, cpuClaim, metav1.CreateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				fixture.By("creating a pod consuming the multi-request claim")
				pod := makeTesterPodWithNamedClaim(fxt.Namespace.Name, dracpuTesterImage, createdClaim.Name, targetNode.Name, publishNodeAllocatableMapping)
				createdPod, err := e2epod.CreateSync(ctx, fxt.K8SClientset, pod)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				fixture.By("verifying the claim allocation expanded into two results for the same request")
				allocatedClaim, err := fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Get(ctx, createdClaim.Name, metav1.GetOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Expect(allocatedClaim).To(resourceclaimmatchers.HaveAllocationResultsForRequest("request-multi", desiredTotalCPUs))
				gomega.Expect(allocatedClaim).To(resourceclaimmatchers.HaveAllocationResultsAllConsuming("dra.cpu/cpu", 1))

				fixture.By("verifying the pod got 2 distinct CPUs with no overlap")
				alloc := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, createdPod)
				fxt.Log.Info("multi-request claim allocation", "cpuAssigned", alloc.CPUAssigned.String())
				gomega.Expect(alloc.CPUAssigned).To(cpusetmatchers.HaveSize(desiredTotalCPUs), "expected 2 distinct CPUs allocated")
				gomega.Expect(alloc.CPUAssigned).To(cpusetmatchers.BeSubsetOf(availableCPUs), "allocated CPUs must be within available set")
			})

			ginkgo.It("should allocate non-overlapping CPUs for request with count > 1 in the same grouped claim", func(ctx context.Context) {
				if cpuDeviceMode != "grouped" {
					ginkgo.Skip("this test only applies to grouped CPU device mode")
				}
				if groupBy == "machine" {
					ginkgo.Skip("skipping this test in machine grouping mode as we do not configure opaque config in claim")
				}
				desiredTotalCPUs := 2
				if availableCPUs.Size() < desiredTotalCPUs {
					ginkgo.Skip("need at least 2 available CPUs for this test")
				}

				fixture.By("creating a ResourceClaim with a requests asking for 1 CPU with count 2")
				cpuClaim := &resourcev1.ResourceClaim{
					ObjectMeta: metav1.ObjectMeta{
						Namespace:    fxt.Namespace.Name,
						GenerateName: "claim-multicount-request-",
					},
					Spec: resourcev1.ResourceClaimSpec{
						Devices: resourcev1.DeviceClaim{
							Requests: []resourcev1.DeviceRequest{
								{
									Name: "request-multi",
									Exactly: &resourcev1.ExactDeviceRequest{
										DeviceClassName: "dra.cpu",
										Count:           int64(desiredTotalCPUs),
										Capacity: &resourcev1.CapacityRequirements{
											Requests: map[resourcev1.QualifiedName]resource.Quantity{
												"dra.cpu/cpu": *resource.NewQuantity(1, resource.DecimalSI),
											},
										},
									},
								},
							},
						},
					},
				}
				createdClaim, err := fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Create(ctx, cpuClaim, metav1.CreateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				fixture.By("creating a pod consuming the multi-request claim")
				pod := makeTesterPodWithNamedClaim(fxt.Namespace.Name, dracpuTesterImage, createdClaim.Name, targetNode.Name, publishNodeAllocatableMapping)
				createdPod, err := e2epod.CreateSync(ctx, fxt.K8SClientset, pod)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				fixture.By("verifying the pod got 2 distinct CPUs with no overlap")
				alloc := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, createdPod)
				fxt.Log.Info("multi-request claim allocation", "cpuAssigned", alloc.CPUAssigned.String())
				gomega.Expect(alloc.CPUAssigned).To(cpusetmatchers.HaveSize(desiredTotalCPUs), "expected 2 distinct CPUs allocated")
				gomega.Expect(alloc.CPUAssigned).To(cpusetmatchers.BeSubsetOf(availableCPUs), "allocated CPUs must be within available set")
			})

			ginkgo.It("should reuse the same grouped device for request with count > 1 when only one device matches", func(ctx context.Context) {
				if cpuDeviceMode != "grouped" {
					ginkgo.Skip("this test only applies to grouped CPU device mode")
				}
				if groupBy != "numanode" {
					// TODO: extend this test to support socket grouping as well.
					ginkgo.Skip("this test currently only applies to NUMA-node grouping")
				}
				desiredTotalCPUs := 2
				if availableCPUs.Size() < desiredTotalCPUs {
					ginkgo.Skip("need at least 2 available CPUs for this test")
				}

				selectorValue := 0
				eligibleCPUs := cpuset.New()
				for _, info := range targetNodeCPUInfo.ByNUMANode() {
					groupCPUs := info.CPUs.Intersection(availableCPUs)
					if groupCPUs.Size() < desiredTotalCPUs {
						continue
					}
					selectorValue = info.NUMANodeID
					eligibleCPUs = groupCPUs
					break
				}
				if eligibleCPUs.Size() == 0 {
					ginkgo.Skip("need one NUMA-node grouped device with at least 2 allocatable CPUs for this test")
				}

				fixture.By("creating a ResourceClaim where only one grouped device is eligible")
				cpuClaim := &resourcev1.ResourceClaim{
					ObjectMeta: metav1.ObjectMeta{
						Namespace:    fxt.Namespace.Name,
						GenerateName: "claim-same-device-multicount-",
					},
					Spec: resourcev1.ResourceClaimSpec{
						Devices: resourcev1.DeviceClaim{
							Requests: []resourcev1.DeviceRequest{
								{
									Name: "request-same-device",
									Exactly: &resourcev1.ExactDeviceRequest{
										DeviceClassName: "dra.cpu",
										Count:           int64(desiredTotalCPUs),
										Capacity: &resourcev1.CapacityRequirements{
											Requests: map[resourcev1.QualifiedName]resource.Quantity{
												"dra.cpu/cpu": *resource.NewQuantity(1, resource.DecimalSI),
											},
										},
										Selectors: []resourcev1.DeviceSelector{
											{
												CEL: &resourcev1.CELDeviceSelector{
													// the 0-th node is always present
													Expression: fmt.Sprintf(`device.attributes["dra.cpu"].numaNodeID == %d`, selectorValue),
												},
											},
										},
									},
								},
							},
						},
					},
				}
				createdClaim, err := fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Create(ctx, cpuClaim, metav1.CreateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				fixture.By("creating a pod consuming the claim")
				pod := makeTesterPodWithNamedClaim(fxt.Namespace.Name, dracpuTesterImage, createdClaim.Name, targetNode.Name, publishNodeAllocatableMapping)
				createdPod, err := e2epod.CreateSync(ctx, fxt.K8SClientset, pod)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				fixture.By("verifying the claim allocation expanded into two results on the same device")
				allocatedClaim, err := fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Get(ctx, createdClaim.Name, metav1.GetOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Expect(allocatedClaim).To(resourceclaimmatchers.ReuseSameDeviceForRequest("request-same-device", desiredTotalCPUs))

				fixture.By("verifying the pod got 2 distinct CPUs from the selected grouped device")
				alloc := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, createdPod)
				fxt.Log.Info("same-device multi-count allocation", "cpuAssigned", alloc.CPUAssigned.String(), "selectorValue", selectorValue)
				gomega.Expect(alloc.CPUAssigned).To(cpusetmatchers.HaveSize(desiredTotalCPUs), "expected 2 distinct CPUs allocated")
				gomega.Expect(alloc.CPUAssigned).To(cpusetmatchers.BeSubsetOf(eligibleCPUs), "allocated CPUs must come from the selected grouped device")
			})

			ginkgo.It("should allocate non-overlapping CPUs for request with count > 1 in the same grouped claim forcing spread", func(ctx context.Context) {
				if cpuDeviceMode != "grouped" {
					ginkgo.Skip("this test only applies to grouped CPU device mode")
				}
				if groupBy != "numanode" {
					ginkgo.Skip("skipping this test because it requires grouping by NUMA node")
				}
				desiredTotalCPUs := 2
				if availableCPUs.Size() < desiredTotalCPUs {
					ginkgo.Skip("need at least 2 available CPUs for this test")
				}
				numaInfo := targetNodeCPUInfo.ByNUMANode()
				if len(numaInfo) < desiredTotalCPUs {
					ginkgo.Skip("need at least 2 available NUMA Nodes for this test")
				}
				// TODO: tighten the check to ensure we have at least 1 free CPU on each NUMA node
				distinctNUMA := resourcev1.FullyQualifiedName("dra.cpu/numaNodeID")

				fixture.By("creating a ResourceClaim with a requests asking for 1 CPU with count 2")
				cpuClaim := &resourcev1.ResourceClaim{
					ObjectMeta: metav1.ObjectMeta{
						Namespace:    fxt.Namespace.Name,
						GenerateName: "claim-multicount-request-",
					},
					Spec: resourcev1.ResourceClaimSpec{
						Devices: resourcev1.DeviceClaim{
							Requests: []resourcev1.DeviceRequest{
								{
									Name: "request-multi",
									Exactly: &resourcev1.ExactDeviceRequest{
										DeviceClassName: "dra.cpu",
										Count:           int64(desiredTotalCPUs),
										Capacity: &resourcev1.CapacityRequirements{
											Requests: map[resourcev1.QualifiedName]resource.Quantity{
												"dra.cpu/cpu": *resource.NewQuantity(1, resource.DecimalSI),
											},
										},
									},
								},
							},
							Constraints: []resourcev1.DeviceConstraint{
								{
									Requests:          []string{"request-multi"},
									DistinctAttribute: &distinctNUMA,
								},
							},
						},
					},
				}
				createdClaim, err := fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Create(ctx, cpuClaim, metav1.CreateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				fixture.By("creating a pod consuming the multi-request claim")
				pod := makeTesterPodWithNamedClaim(fxt.Namespace.Name, dracpuTesterImage, createdClaim.Name, targetNode.Name, publishNodeAllocatableMapping)
				createdPod, err := e2epod.CreateSync(ctx, fxt.K8SClientset, pod)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())

				fixture.By("verifying the pod got 2 distinct CPUs with no overlap")
				alloc := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, createdPod)
				fxt.Log.Info("multi-request claim allocation", "cpuAssigned", alloc.CPUAssigned.String())
				gomega.Expect(alloc.CPUAssigned).To(cpusetmatchers.HaveSize(desiredTotalCPUs), "expected 2 distinct CPUs allocated")
				gomega.Expect(alloc.CPUAssigned).To(cpusetmatchers.BeSubsetOf(availableCPUs), "allocated CPUs must be within available set")
				gomega.Expect(alloc.CPUAssigned).To(cpusetmatchers.BeDistributedAcrossNUMANodes(numaInfo, desiredTotalCPUs), "allocated CPUs must be evenly spread across NUMA nodes")
			})

			ginkgo.It("should allocate exclusive CPUs using opaque config for machine grouping mode", func(ctx context.Context) {
				if cpuDeviceMode != "grouped" || groupBy != "machine" {
					ginkgo.Skip("this test only applies to grouped CPU device mode with machine grouping")
				}
				if availableCPUs.Size() < 3 {
					ginkgo.Skip("need at least 3 available CPUs for this test")
				}

				availableList := availableCPUs.UnsortedList()
				claim1CPUs := cpuset.New(availableList[0])
				claim2CPUs := cpuset.New(availableList[1])

				claimCPUs := claim1CPUs.Union(claim2CPUs)
				expectedSharedCPUs := availableCPUs.Difference(claimCPUs)

				fixture.By("creating a best-effort pod")
				shrPod1 := mustCreateBestEffortPod(ctx, fxt, targetNode.Name, dracpuTesterImage)
				gomega.Eventually(observeAssignedCPUs(ctx, fxt, shrPod1)).WithTimeout(1*time.Minute).WithPolling(5*time.Second).Should(cpusetmatchers.Equal(availableCPUs), "the best-effort tester pod %s does not have access to the exclusively allocated CPUs", e2epod.Identify(shrPod1))

				claimsAndCPUSets := []struct {
					name   string
					cpuset cpuset.CPUSet
				}{
					{name: "claim-machine-0", cpuset: claim1CPUs},
					{name: "claim-machine-1", cpuset: claim2CPUs},
				}

				var exclPods []*v1.Pod
				for _, tc := range claimsAndCPUSets {
					fixture.By("creating ResourceClaim %s with cpuset %s", tc.name, tc.cpuset.String())

					claim := &resourcev1.ResourceClaim{
						ObjectMeta: metav1.ObjectMeta{
							Namespace: fxt.Namespace.Name,
							Name:      tc.name,
						},
						Spec: makeResourceClaimSpecWithOpaqueConfig(1, true, tc.cpuset.String()),
					}
					_, err := fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Create(ctx, claim, metav1.CreateOptions{})
					gomega.Expect(err).ToNot(gomega.HaveOccurred())

					fixture.By("creating pod referencing %s", tc.name)
					pod := makeTesterPodWithNamedClaim(fxt.Namespace.Name, dracpuTesterImage, tc.name, targetNode.Name, publishNodeAllocatableMapping)
					createdPod, err := e2epod.CreateSync(ctx, fxt.K8SClientset, pod)
					gomega.Expect(err).ToNot(gomega.HaveOccurred())
					exclPods = append(exclPods, createdPod)
				}

				fixture.By("verifying CPU assignments for each pod match their custom cpuset overrides")
				for i, pod := range exclPods {
					alloc := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, pod)
					expectedSet := claimsAndCPUSets[i].cpuset
					fxt.Log.Info("pod allocation verification", "pod", pod.Name, "assigned", alloc.CPUAssigned.String(), "expected", expectedSet.String())
					gomega.Expect(alloc.CPUAssigned).To(cpusetmatchers.Equal(expectedSet))
				}

				fixture.By("creating a second best-effort pod")
				shrPod2 := mustCreateBestEffortPod(ctx, fxt, targetNode.Name, dracpuTesterImage)
				gomega.Eventually(observeAssignedCPUs(ctx, fxt, shrPod2)).WithTimeout(1*time.Minute).WithPolling(5*time.Second).Should(cpusetmatchers.Equal(expectedSharedCPUs), "the best-effort tester pod %s does not have access to the exclusively allocated CPUs", e2epod.Identify(shrPod2))

				ginkgo.By("checking the CPU pool of the best-effort pod created before the pods with CPU resource claims")
				gomega.Eventually(observeAssignedCPUs(ctx, fxt, shrPod1)).WithTimeout(1*time.Minute).WithPolling(5*time.Second).Should(cpusetmatchers.Equal(expectedSharedCPUs), "the best-effort tester pod %s does not have access to the exclusively allocated CPUs", e2epod.Identify(shrPod1))

				fixture.By("deleting the pods with exclusive CPUs")
				for _, pod := range exclPods {
					gomega.Expect(e2epod.DeleteSync(ctx, fxt.K8SClientset, pod)).To(gomega.Succeed(), "cannot delete pod %s", e2epod.Identify(pod))
				}

				ginkgo.By("checking existing shared containers keep their last cpuset until the next CreateContainer or Synchronize")
				gomega.Eventually(observeAssignedCPUs(ctx, fxt, shrPod1)).WithTimeout(1*time.Minute).WithPolling(5*time.Second).Should(cpusetmatchers.Equal(expectedSharedCPUs), "the best-effort tester pod %s does not have access to the exclusively allocated CPUs", e2epod.Identify(shrPod1))
				gomega.Eventually(observeAssignedCPUs(ctx, fxt, shrPod2)).WithTimeout(1*time.Minute).WithPolling(5*time.Second).Should(cpusetmatchers.Equal(expectedSharedCPUs), "the best-effort tester pod %s does not have access to the exclusively allocated CPUs", e2epod.Identify(shrPod2))
			})
		})
	})
})

func observeAssignedCPUs(ctx context.Context, fxt *fixture.Fixture, pod *v1.Pod) func() cpuset.CPUSet {
	return func() cpuset.CPUSet {
		alloc := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, pod)
		fxt.Log.Info("checking shared allocation", "pod", e2epod.Identify(pod), "cpuAllocated", alloc.CPUAssigned.String(), "cpuAffinity", alloc.CPUAffinity.String())
		return alloc.CPUAssigned
	}
}
