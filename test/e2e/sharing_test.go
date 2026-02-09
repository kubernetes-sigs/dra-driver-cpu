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
	"k8s.io/utils/cpuset"
	"k8s.io/utils/ptr"
)

var _ = ginkgo.Describe("Claim sharing", ginkgo.Serial, ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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
		gomega.Expect(dsReservedCPUs.Equals(reservedCPUs)).To(gomega.BeTrue(), "daemonset reserved cpus %v do not match test reserved cpus %v", dsReservedCPUs.String(), reservedCPUs.String())

		rootFxt.Log.Info("daemonset configuration", "reservedCPUs", dsReservedCPUs.String(), "deviceMode", cpuDeviceMode)

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

		infoPod := discovery.MakePod(infraFxt.Namespace.Name, dracpuTesterImage)
		infoPod = e2epod.PinToNode(infoPod, targetNode.Name)
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

	ginkgo.When("sharing a CPU claim", func() {
		var fxt *fixture.Fixture
		var claim *resourcev1.ResourceClaim

		ginkgo.BeforeEach(func(ctx context.Context) {
			fxt = rootFxt.WithPrefix("sharingcpu")
			gomega.Expect(fxt.Setup(ctx)).To(gomega.Succeed())

			fixture.By("creating a cpu ResourceClaim on %q", fxt.Namespace.Name)
			cpuClaim := resourcev1.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: fxt.Namespace.Name,
					Name:      "claim-cpu-1",
				},
				Spec: makeResourceClaimSpec(1, cpuDeviceMode == "grouped"),
			}

			var err error
			claim, err = fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Create(ctx, &cpuClaim, metav1.CreateOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(claim).ToNot(gomega.BeNil())
		})

		ginkgo.AfterEach(func(ctx context.Context) {
			gomega.Expect(fxt.Teardown(ctx)).To(gomega.Succeed())
		})

		ginkgo.It("should fail to run pods which share a claim", func(ctx context.Context) {
			testPod := v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace:    fxt.Namespace.Name,
					GenerateName: "pod-with-cpu-claim-",
				},
				Spec: v1.PodSpec{
					RestartPolicy: v1.RestartPolicyNever,
					Containers: []v1.Container{
						{
							Name:    "container-with-cpu-1",
							Image:   dracpuTesterImage,
							Command: []string{"/dracputester"},
							Resources: v1.ResourceRequirements{
								Limits: v1.ResourceList{
									v1.ResourceCPU:    *resource.NewQuantity(1, resource.DecimalSI),
									v1.ResourceMemory: *resource.NewQuantity(256*(1<<20), resource.BinarySI),
								},
								Claims: []v1.ResourceClaim{
									{
										Name: "cpu",
									},
								},
							},
						},
					},
					ResourceClaims: []v1.PodResourceClaim{
						{
							Name:              "cpu",
							ResourceClaimName: ptr.To(claim.Name),
						},
					},
				},
			}

			fixture.By("creating a pod consuming the ResourceClaim on %q", fxt.Namespace.Name)
			createdPod1, err := e2epod.CreateSync(ctx, fxt.K8SClientset, &testPod)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Expect(createdPod1).ToNot(gomega.BeNil())

			fixture.By("creating a second pod consuming the ResourceClaim on %q, ensuring it gets ContainerCreate Error", fxt.Namespace.Name)
			createdPod2, err := fxt.K8SClientset.CoreV1().Pods(testPod.Namespace).Create(ctx, &testPod, metav1.CreateOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Eventually(func() *v1.Pod {
				pod, err := fxt.K8SClientset.CoreV1().Pods(createdPod2.Namespace).Get(ctx, createdPod2.Name, metav1.GetOptions{})
				if err != nil {
					return nil
				}
				return pod
			}).WithTimeout(time.Minute).WithPolling(2 * time.Second).Should(BeFailedToCreate(fxt))
		})

		ginkgo.It("should fail to run a pod with multiple containers which share a claim", ginkgo.Label("negative"), func(ctx context.Context) {
			testPod := v1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: fxt.Namespace.Name,
					Name:      "pod-with-cpu-claim-multicnt",
				},
				Spec: v1.PodSpec{
					RestartPolicy: v1.RestartPolicyNever,
					Containers: []v1.Container{
						{
							Name:    "container-with-cpu-1",
							Image:   dracpuTesterImage,
							Command: []string{"/dracputester"},
							Resources: v1.ResourceRequirements{
								Limits: v1.ResourceList{
									v1.ResourceCPU:    *resource.NewQuantity(1, resource.DecimalSI),
									v1.ResourceMemory: *resource.NewQuantity(256*(1<<20), resource.BinarySI),
								},
								Claims: []v1.ResourceClaim{
									{
										Name: "cpu",
									},
								},
							},
						},
						{
							Name:    "container-with-cpu-2",
							Image:   dracpuTesterImage,
							Command: []string{"/dracputester"},
							Resources: v1.ResourceRequirements{
								Limits: v1.ResourceList{
									v1.ResourceCPU:    *resource.NewQuantity(1, resource.DecimalSI),
									v1.ResourceMemory: *resource.NewQuantity(256*(1<<20), resource.BinarySI),
								},
								Claims: []v1.ResourceClaim{
									{
										Name: "cpu",
									},
								},
							},
						},
					},
					ResourceClaims: []v1.PodResourceClaim{
						{
							Name:              "cpu",
							ResourceClaimName: ptr.To(claim.Name),
						},
					},
				},
			}

			fixture.By("creating a pod with multiple containers consuming the ResourceClaim on %q, ensuring it gets ContainerCreateError", fxt.Namespace.Name)
			createdPod, err := fxt.K8SClientset.CoreV1().Pods(testPod.Namespace).Create(ctx, &testPod, metav1.CreateOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			gomega.Eventually(func() *v1.Pod {
				pod, err := fxt.K8SClientset.CoreV1().Pods(createdPod.Namespace).Get(ctx, createdPod.Name, metav1.GetOptions{})
				if err != nil {
					return nil
				}
				return pod
			}).WithTimeout(time.Minute).WithPolling(2 * time.Second).Should(BeFailedToCreate(fxt))
		})
	})
})
