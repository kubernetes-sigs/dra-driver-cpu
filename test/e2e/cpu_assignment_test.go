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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/cpuset"
	"k8s.io/utils/ptr"
)

const (
	minCPUCountForExclusiveAllocation = 4
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
var _ = ginkgo.Describe("CPU Assignment", ginkgo.Serial, ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var rootFxt *fixture.Fixture
	var targetNode *v1.Node
	var targetNodeCPUInfo discovery.DRACPUInfo
	var availableCPUs cpuset.CPUSet
	var dracpuTesterImage string
	var reservedCPUs cpuset.CPUSet

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

		if targetNodeName := os.Getenv("DRACPU_E2E_TARGET_NODE"); len(targetNodeName) > 0 {
			targetNode, err = rootFxt.K8SClientset.CoreV1().Nodes().Get(ctx, targetNodeName, metav1.GetOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get worker node %q: %v", targetNodeName, err)
		} else {
			workerNodes, err := node.FindWorkers(ctx, infraFxt.K8SClientset)
			gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot find worker nodes: %v", err)
			gomega.Expect(workerNodes).ToNot(gomega.BeEmpty(), "no worker nodes detected")
			targetNode = workerNodes[0] // pick random one, this is the simplest random pick
		}
		rootFxt.Log.Info("using worker node", "nodeName", targetNode.Name)

		infoPod := makeDiscoveryPod(infraFxt.Namespace.Name, dracpuTesterImage)
		infoPod = pinPodToNode(infoPod, targetNode.Name)
		infoPod, err = e2epod.RunToCompletion(ctx, infraFxt.K8SClientset, infoPod)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create discovery pod: %v", err)
		data, err := e2epod.GetLogs(infraFxt.K8SClientset, ctx, infoPod.Namespace, infoPod.Name, infoPod.Spec.Containers[0].Name)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get logs from discovery pod: %v", err)
		gomega.Expect(json.Unmarshal([]byte(data), &targetNodeCPUInfo)).To(gomega.Succeed())

		allocatableCPUs := makeCPUSetFromDiscoveredCPUInfo(targetNodeCPUInfo)
		availableCPUs = allocatableCPUs.Difference(reservedCPUs)
		rootFxt.Log.Info("checking worker node", "nodeName", infoPod.Spec.NodeName, "coreCount", len(targetNodeCPUInfo.CPUs), "allocatableCPUs", allocatableCPUs.String(), "availableCPUs", availableCPUs.String())
	})

	ginkgo.When("setting resource claims", func() {
		var fxt *fixture.Fixture
		var exclCPUClaim *resourcev1.ResourceClaim

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
				fixture.By("checking the target nodes has at least %d allocatable cpus", minCPUCountForExclusiveAllocation)
				if len(targetNodeCPUInfo.CPUs) < minCPUCountForExclusiveAllocation {
					ginkgo.Skip(fmt.Sprintf("exclusive allocation tests require at least %d cpus in the worker node", minCPUCountForExclusiveAllocation))
				}

				cpuCount := 2
				fixture.By("creating a resource claim for %d cpus", cpuCount)

				claim := resourcev1.ResourceClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name: "cpu-request-2-exclusive",
					},
					Spec: resourcev1.ResourceClaimSpec{
						Devices: resourcev1.DeviceClaim{
							Requests: []resourcev1.DeviceRequest{
								{
									Name: "request-cpus",
									Exactly: &resourcev1.ExactDeviceRequest{
										DeviceClassName: "dra.cpu",
										Count:           int64(cpuCount),
									},
								},
							},
						},
					},
				}

				var err error
				exclCPUClaim, err = fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Create(ctx, &claim, metav1.CreateOptions{})
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
				gomega.Expect(exclCPUClaim).ToNot(gomega.BeNil())
			})

			ginkgo.It("should allocate exclusive CPUs, remove from the shared pool, release on pod deletion", func(ctx context.Context) {
				var err error

				fixture.By("creating a best-effort reference pod")
				shrPod1 := makeTesterPodBestEffort(fxt.Namespace.Name, dracpuTesterImage)
				shrPod1 = pinPodToNode(shrPod1, targetNode.Name)
				shrPod1, err = e2epod.CreateSync(ctx, fxt.K8SClientset, shrPod1)
				gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create tester pod: %v", err)

				fixture.By("checking the best-effort reference pod %s has access to all the non-reserved node CPUs through the shared pool", identifyPod(shrPod1))
				sharedAllocPre := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, shrPod1)
				fxt.Log.Info("checking shared allocation", "pod", identifyPod(shrPod1), "cpuAllocated", sharedAllocPre.CPUAssigned.String(), "cpuAffinity", sharedAllocPre.CPUAffinity.String())
				unallocatedCPUs := availableCPUs.Difference(sharedAllocPre.CPUAssigned)
				gomega.Expect(unallocatedCPUs.Size()).To(gomega.BeZero(), "available CPUs not in the shared pool: %v", unallocatedCPUs.String())

				fixture.By("creating a guaranteed pod getting exclusive CPUs from the resource claim")
				exclPod := makeTesterPodWithExclusiveCPUClaim(fxt.Namespace.Name, dracpuTesterImage, exclCPUClaim)
				exclPod = pinPodToNode(exclPod, targetNode.Name)
				exclPod, err = e2epod.CreateSync(ctx, fxt.K8SClientset, exclPod)
				gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create tester pod: %v", err)

				fixture.By("checking the pod %s with exclusive CPUs has access to amount of CPUs requested in the claim", identifyPod(exclPod))
				gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get the CPUs allocated to tester pod %s", identifyPod(exclPod))
				exclusiveAlloc := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, exclPod)
				gomega.Expect(int64(exclusiveAlloc.CPUAssigned.Size())).To(gomega.Equal(exclCPUClaim.Spec.Devices.Requests[0].Exactly.Count))
				fxt.Log.Info("checking exclusive allocation", "pod", identifyPod(exclPod), "cpus", exclusiveAlloc.CPUAssigned.String(), "sharedPool", sharedAllocPre.CPUAssigned.String(), "unallocated", unallocatedCPUs.String())

				fixture.By("checking the shared pool does not include anymore the exclusively allocated CPUs")
				expectedSharedCPUs := availableCPUs.Difference(exclusiveAlloc.CPUAssigned)

				fixture.By("creating a second best-effort reference pod")
				shrPod2 := makeTesterPodBestEffort(fxt.Namespace.Name, dracpuTesterImage)
				shrPod2 = pinPodToNode(shrPod2, targetNode.Name)
				shrPod2, err = e2epod.CreateSync(ctx, fxt.K8SClientset, shrPod2)
				gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create the second tester pod: %v", err)

				verifySharedPoolMatches(ctx, fxt, shrPod1, expectedSharedCPUs)
				verifySharedPoolMatches(ctx, fxt, shrPod2, expectedSharedCPUs)

				fixture.By("deleting the guaranteed pod %s with exclusive CPUs from the resource claim", identifyPod(exclPod))
				gomega.Expect(e2epod.DeleteSync(ctx, fxt.K8SClientset, exclPod.Namespace, exclPod.Name)).To(gomega.Succeed())

				verifySharedPoolMatches(ctx, fxt, shrPod1, availableCPUs)
				verifySharedPoolMatches(ctx, fxt, shrPod2, availableCPUs)
			})

			ginkgo.It("should keep exclusively allocated CPUs across container restarts", func(ctx context.Context) {
				var err error

				fixture.By("creating a best-effort reference pod")
				shrPod1 := makeTesterPodBestEffort(fxt.Namespace.Name, dracpuTesterImage)
				shrPod1 = pinPodToNode(shrPod1, targetNode.Name)
				shrPod1, err = e2epod.CreateSync(ctx, fxt.K8SClientset, shrPod1)
				gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create tester pod: %v", err)

				fixture.By("checking the best-effort reference pod %s has access to all the non-reserved node CPUs through the shared pool", identifyPod(shrPod1))
				sharedAllocPre := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, shrPod1)
				fxt.Log.Info("checking shared allocation", "pod", identifyPod(shrPod1), "cpuAllocated", sharedAllocPre.CPUAssigned.String(), "cpuAffinity", sharedAllocPre.CPUAffinity.String())
				unallocatedCPUs := availableCPUs.Difference(sharedAllocPre.CPUAssigned)
				gomega.Expect(unallocatedCPUs.Size()).To(gomega.BeZero(), "available CPUs not in the shared pool: %v", unallocatedCPUs.String())

				fixture.By("creating a guaranteed pod getting exclusive CPUs from the resource claim")
				exclPod := makeTesterPodWithExclusiveCPUClaim(fxt.Namespace.Name, dracpuTesterImage, exclCPUClaim)
				exclPod = pinPodToNode(exclPod, targetNode.Name)
				exclPod.Spec.Containers[0].Args = []string{"-run-for", "2s"}
				exclPod, err = e2epod.CreateSync(ctx, fxt.K8SClientset, exclPod)
				gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create tester pod: %v", err)

				fixture.By("checking the pod %s with exclusive CPUs has access to amount of CPUs requested in the claim", identifyPod(exclPod))
				gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get the CPUs allocated to tester pod %s", identifyPod(exclPod))
				exclusiveAlloc := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, exclPod)
				gomega.Expect(int64(exclusiveAlloc.CPUAssigned.Size())).To(gomega.Equal(exclCPUClaim.Spec.Devices.Requests[0].Exactly.Count))
				fxt.Log.Info("checking exclusive allocation", "pod", identifyPod(exclPod), "cpus", exclusiveAlloc.CPUAssigned.String(), "sharedPool", sharedAllocPre.CPUAssigned.String(), "unallocated", unallocatedCPUs.String())

				fixture.By("checking the pod %s with exclusive CPUs retain access to CPUs across restarts", identifyPod(exclPod))
				gomega.Consistently(func() error {
					exclusiveAllocUpdated := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, exclPod)
					fxt.Log.Info("checking exclusive allocation", "pod", identifyPod(exclPod), "cpuAllocated", exclusiveAllocUpdated.CPUAssigned.String(), "cpuAffinity", exclusiveAllocUpdated.CPUAffinity.String())
					if !exclusiveAlloc.CPUAssigned.Equals(exclusiveAllocUpdated.CPUAssigned) {
						return fmt.Errorf("exclusive CPUs mismatch: expected %v got %v", exclusiveAlloc.CPUAssigned.String(), exclusiveAllocUpdated.CPUAssigned.String())
					}
					return nil
				}).WithTimeout(30*time.Second).WithPolling(1*time.Second).Should(gomega.Succeed(), "the tester pod %s with exclusive CPU claims retain CPUs", identifyPod(exclPod))

				//				fixture.By("checking the shared pool does not include anymore the exclusively allocated CPUs")
				//				expectedSharedCPUs := availableCPUs.Difference(exclusiveAlloc.CPUAssigned)
			})
		})
	})
})

func verifySharedPoolMatches(ctx context.Context, fxt *fixture.Fixture, sharedPod *v1.Pod, expectedSharedCPUs cpuset.CPUSet) {
	ginkgo.GinkgoHelper()

	fixture.By("checking the CPU pool of the best-effort tester pod %s matches expected %s", identifyPod(sharedPod), expectedSharedCPUs.String())
	gomega.Eventually(func() error {
		sharedAllocUpdated := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, sharedPod)
		fxt.Log.Info("checking shared allocation", "pod", identifyPod(sharedPod), "cpuAllocated", sharedAllocUpdated.CPUAssigned.String(), "cpuAffinity", sharedAllocUpdated.CPUAffinity.String())
		if !expectedSharedCPUs.Equals(sharedAllocUpdated.CPUAssigned) {
			return fmt.Errorf("shared CPUs mismatch: expected %v got %v", expectedSharedCPUs.String(), sharedAllocUpdated.CPUAssigned.String())
		}
		return nil
	}).WithTimeout(1*time.Minute).WithPolling(5*time.Second).Should(gomega.Succeed(), "the best-effort tester pod %s does not have access to the exclusively allocated CPUs", identifyPod(sharedPod))
}

func makeCPUSetFromDiscoveredCPUInfo(cpuInfo discovery.DRACPUInfo) cpuset.CPUSet {
	coreIDs := make([]int, len(cpuInfo.CPUs))
	for idx, cpu := range cpuInfo.CPUs {
		coreIDs[idx] = cpu.CpuID
	}
	return cpuset.New(coreIDs...)
}

type CPUAllocation struct {
	CPUAssigned cpuset.CPUSet
	CPUAffinity cpuset.CPUSet
}

func getTesterPodCPUAllocation(cs kubernetes.Interface, ctx context.Context, pod *v1.Pod) CPUAllocation {
	ginkgo.GinkgoHelper()

	data, err := e2epod.GetLogs(cs, ctx, pod.Namespace, pod.Name, pod.Spec.Containers[0].Name)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get logs for %s/%s/%s", pod.Namespace, pod.Name, pod.Spec.Containers[0].Name)

	testerInfo := discovery.DRACPUTester{}
	gomega.Expect(json.Unmarshal([]byte(data), &testerInfo)).To(gomega.Succeed())

	ret := CPUAllocation{}
	ret.CPUAssigned, err = cpuset.Parse(testerInfo.Allocation.CPUs)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot parse assigned cpuset: %q", testerInfo.Allocation.CPUs)
	ret.CPUAffinity, err = cpuset.Parse(testerInfo.Runtimeinfo.CPUAffinity)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot parse affinity cpuset: %q", testerInfo.Runtimeinfo.CPUAffinity)
	return ret
}

func makeTesterPodWithExclusiveCPUClaim(ns, image string, cpuClaim *resourcev1.ResourceClaim) *v1.Pod {
	ginkgo.GinkgoHelper()
	cpuQty := resource.NewQuantity(cpuClaim.Spec.Devices.Requests[0].Exactly.Count, resource.DecimalSI)
	memQty, err := resource.ParseQuantity("256Mi") // random "low enough" value
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "tester-pod-excl-cpu-claim-",
			Namespace:    ns,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:    "tester-container-1",
					Image:   image,
					Command: []string{"/dracputester"},
					Resources: v1.ResourceRequirements{
						Requests: v1.ResourceList{
							v1.ResourceCPU:    *cpuQty,
							v1.ResourceMemory: memQty,
						},
						Limits: v1.ResourceList{
							v1.ResourceCPU:    *cpuQty,
							v1.ResourceMemory: memQty,
						},
						Claims: []v1.ResourceClaim{
							{
								Name: "tester-container-1-claim",
							},
						},
					},
				},
			},
			ResourceClaims: []v1.PodResourceClaim{
				{
					Name:              "tester-container-1-claim",
					ResourceClaimName: ptr.To(cpuClaim.Name),
				},
			},
			RestartPolicy: v1.RestartPolicyAlways,
		},
	}
}

func makeTesterPodBestEffort(ns, image string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "tester-pod-be-",
			Namespace:    ns,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:    "tester-container",
					Image:   image,
					Command: []string{"/dracputester"},
					// at the moment we depend on pod logs to learn the cpu allocation.
					// Therefore, the pod without resource claims, best-effort,
					// will restart periodically to provide the up to date information.
					// NOTE: restarting always ensuring there's the minimal and easiest
					// amount of logs to process. The other option would be to periodically
					// log the data, but then the client side will need to read and discard
					// all but the latest update, which is wasteful. This approach is the simplest.
					Args: []string{"-run-for", "1s"},
				},
			},
			RestartPolicy: v1.RestartPolicyAlways,
		},
	}
}

func makeDiscoveryPod(ns, image string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "discovery-pod",
			Namespace: ns,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:            "discovery-container",
					Image:           image,
					ImagePullPolicy: v1.PullIfNotPresent,
					Command:         []string{"/dracpuinfo"},
				},
			},
			RestartPolicy: v1.RestartPolicyNever,
		},
	}
}

func pinPodToNode(pod *v1.Pod, nodeName string) *v1.Pod {
	pod.Spec.NodeSelector = map[string]string{
		"kubernetes.io/hostname": nodeName,
	}
	return pod
}

func identifyPod(pod *v1.Pod) string {
	return pod.Namespace + "/" + pod.Name + "@" + pod.Spec.NodeName
}
