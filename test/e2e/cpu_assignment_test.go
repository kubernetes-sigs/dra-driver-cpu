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
	"strings"
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
		rootFxt           *fixture.Fixture
		targetNode        *v1.Node
		targetNodeCPUInfo discovery.DRACPUInfo
		availableCPUs     cpuset.CPUSet
		dracpuTesterImage string
		reservedCPUs      cpuset.CPUSet
		cpuDeviceMode     string
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

		if targetNodeName := os.Getenv("DRACPU_E2E_TARGET_NODE"); len(targetNodeName) > 0 {
			targetNode, err = rootFxt.K8SClientset.CoreV1().Nodes().Get(ctx, targetNodeName, metav1.GetOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get worker node %q: %v", targetNodeName, err)
		} else {
			gomega.Eventually(func() error {
				workerNodes, err := node.FindWorkers(ctx, infraFxt.K8SClientset)
				if err != nil {
					return err
				}
				if len(workerNodes) == 0 {
					return fmt.Errorf("no worker nodes detected")
				}
				targetNode = workerNodes[0] // pick random one, this is the simplest random pick
				return nil
			}).WithTimeout(1*time.Minute).WithPolling(5*time.Second).Should(gomega.Succeed(), "failed to find any worker node")
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
		if reservedCPUs.Size() > 0 {
			gomega.Expect(availableCPUs.Intersection(reservedCPUs).Size()).To(gomega.BeZero(), "available cpus %v overlap with reserved cpus %v", availableCPUs.String(), reservedCPUs.String())
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

				fixture.By("checking the best-effort reference pod %s has access to all the non-reserved node CPUs through the shared pool", identifyPod(shrPod1))
				sharedAllocPre := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, shrPod1)
				fxt.Log.Info("checking shared allocation", "pod", identifyPod(shrPod1), "cpuAllocated", sharedAllocPre.CPUAssigned.String(), "cpuAffinity", sharedAllocPre.CPUAffinity.String())

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
				allAllocatedCPUs := cpuset.New()

				claimName := fmt.Sprintf("cpu-request-%d-excl", cpusPerClaim)
				claimTemplate := makeResourceClaimTemplate(claimName, cpusPerClaim, cpuDeviceMode == "grouped")
				createdClaimTemplate, err := fxt.K8SClientset.ResourceV1().ResourceClaimTemplates(fxt.Namespace.Name).Create(ctx, &claimTemplate, metav1.CreateOptions{})
				for i := 0; i < numPods; i++ {
					gomega.Expect(err).ToNot(gomega.HaveOccurred())
					pod := makeTesterPodWithExclusiveCPUClaim(fxt.Namespace.Name, dracpuTesterImage, createdClaimTemplate)
					createdPod, err := e2epod.CreateSync(ctx, fxt.K8SClientset, pod)
					gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create tester pod %d: %v", i, err)
					exclPods = append(exclPods, createdPod)
				}

				fixture.By("Verifying CPU allocations for each exclusive pod")
				for i, pod := range exclPods {
					alloc := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, pod)
					fxt.Log.Info("Checking exclusive CPU allocation", "pod", identifyPod(pod), "cpuAllocated", alloc.CPUAssigned.String())
					gomega.Expect(alloc.CPUAssigned.Size()).To(gomega.Equal(cpusPerClaim), "Pod %d did not get %d CPUs", i, cpusPerClaim)
					gomega.Expect(alloc.CPUAssigned.IsSubsetOf(availableCPUs)).To(gomega.BeTrue(), "Pod %d got CPUs outside available set", i)
					gomega.Expect(allAllocatedCPUs.Intersection(alloc.CPUAssigned).Size()).To(gomega.Equal(0), "Pod %d has overlapping CPUs", i)
					allAllocatedCPUs = allAllocatedCPUs.Union(alloc.CPUAssigned)
				}
				gomega.Expect(allAllocatedCPUs.Size()).To(gomega.Equal(numPods * cpusPerClaim))
				rootFxt.Log.Info("All exclusive allocation", "pod", "exclusive CPUs", allAllocatedCPUs.String(), "expected Shared CPUs", availableCPUs.Difference(allAllocatedCPUs).String())

				fixture.By("checking the shared pool does not include anymore the exclusively allocated CPUs")
				expectedSharedCPUs := availableCPUs.Difference(allAllocatedCPUs)

				fixture.By("creating a second best-effort reference pod")
				shrPod2 := mustCreateBestEffortPod(ctx, fxt, targetNode.Name, dracpuTesterImage)
				verifySharedPoolMatches(ctx, fxt, shrPod2, expectedSharedCPUs)

				ginkgo.By("checking the CPU pool of the best-effort pod created before the pods with CPU resource claims")
				verifySharedPoolMatches(ctx, fxt, shrPod1, expectedSharedCPUs)

				fixture.By("deleting the pods with exclusive CPUs")
				for _, pod := range exclPods {
					gomega.Expect(e2epod.DeleteSync(ctx, fxt.K8SClientset, pod)).To(gomega.Succeed(), "cannot delete pod %s", identifyPod(pod))
				}

				verifySharedPoolMatches(ctx, fxt, shrPod1, availableCPUs)
				verifySharedPoolMatches(ctx, fxt, shrPod2, availableCPUs)
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

func makeTesterPodWithExclusiveCPUClaim(ns, image string, cpuClaimTemplate *resourcev1.ResourceClaimTemplate) *v1.Pod {
	ginkgo.GinkgoHelper()
	cpuQty := resource.NewQuantity(cpuClaimTemplate.Spec.Spec.Devices.Requests[0].Exactly.Count, resource.DecimalSI)
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
					Name:                      "tester-container-1-claim",
					ResourceClaimTemplateName: ptr.To(cpuClaimTemplate.Name),
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
					Args: []string{"-run-for", "10s"},
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

func mustCreateBestEffortPod(ctx context.Context, fxt *fixture.Fixture, nodeName, dracpuTesterImage string) *v1.Pod {
	fixture.By("creating a best-effort reference pod")
	pod := makeTesterPodBestEffort(fxt.Namespace.Name, dracpuTesterImage)
	pod = pinPodToNode(pod, nodeName)
	pod, err := e2epod.CreateSync(ctx, fxt.K8SClientset, pod)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create tester pod: %v", err)
	return pod
}

func parseReservedCPUsArg(arg string) (string, bool) {
	prefix := "--reserved-cpus="
	if !strings.HasPrefix(arg, prefix) {
		return "", false
	}
	return strings.TrimPrefix(arg, prefix), true
}

func parseCPUDeviceModeArg(arg string) (string, bool) {
	prefix := "--cpu-device-mode="
	if !strings.HasPrefix(arg, prefix) {
		return "", false
	}
	return strings.TrimPrefix(arg, prefix), true
}

func makeResourceClaimTemplate(templateName string, cpus int, isConsumable bool) resourcev1.ResourceClaimTemplate {
	var claimSpec resourcev1.ResourceClaimSpec
	if isConsumable {
		claimSpec = resourcev1.ResourceClaimSpec{
			Devices: resourcev1.DeviceClaim{
				Requests: []resourcev1.DeviceRequest{
					{
						Name: "request-cpus",
						Exactly: &resourcev1.ExactDeviceRequest{
							DeviceClassName: "dra.cpu",
							Capacity: &resourcev1.CapacityRequirements{
								Requests: map[resourcev1.QualifiedName]resource.Quantity{
									"dra.cpu/cpu": resource.MustParse(fmt.Sprintf("%d", cpus)),
								},
							},
						},
					},
				},
			},
		}
	} else {
		claimSpec = resourcev1.ResourceClaimSpec{
			Devices: resourcev1.DeviceClaim{
				Requests: []resourcev1.DeviceRequest{
					{
						Name: "request-cpus",
						Exactly: &resourcev1.ExactDeviceRequest{
							DeviceClassName: "dra.cpu",
							Count:           int64(cpus),
						},
					},
				},
			},
		}
	}

	template := resourcev1.ResourceClaimTemplate{
		ObjectMeta: metav1.ObjectMeta{
			Name: templateName,
		},
		Spec: resourcev1.ResourceClaimTemplateSpec{
			Spec: claimSpec,
		},
	}
	return template
}
