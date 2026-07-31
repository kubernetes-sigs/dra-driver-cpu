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

	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/discovery"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	cpusetmatchers "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/matchers/cpuset"
	e2epod "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/pod"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/cpuset"
)

var _ = ginkgo.Describe("Claim sharing", ginkgo.Serial, ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		rootFxt           *fixture.Fixture
		targetNodeCPUInfo discovery.DRACPUInfo
		availableCPUs     cpuset.CPUSet
		dracpuTesterImage string
		reservedCPUs      cpuset.CPUSet
		cpuDeviceMode     string
		groupBy           string
	)

	ginkgo.BeforeAll(func(ctx context.Context) {
		// early cheap check before to create the Fixture, so we use GinkgoLogr directly
		dracpuTesterImage = requireEnvOrSkip("DRACPU_E2E_TEST_IMAGE")
		ginkgo.GinkgoLogr.Info("discovery image", "pullSpec", dracpuTesterImage)

		reservedCPUs = parseOptionalCPUSetEnv("DRACPU_E2E_RESERVED_CPUS")
		if reservedCPUs.Size() > 0 {
			ginkgo.GinkgoLogr.Info("reserved CPUs", "value", reservedCPUs.String())
		}

		rootFxt = mustCreateFixture()
		infraFxt := rootFxt.WithPrefix("infra")
		mustSetupFixture(ctx, infraFxt)
		ginkgo.DeferCleanup(infraFxt.Teardown)

		ginkgo.By("checking the daemonset configuration matches the test configuration")
		driverConfig := mustLoadDriverDaemonSetConfig(ctx, rootFxt)
		cpuDeviceMode = driverConfig.CPUDeviceMode
		groupBy = driverConfig.GroupBy
		gomega.Expect(driverConfig.ReservedCPUs).To(cpusetmatchers.Equal(reservedCPUs), "daemonset reserved cpus do not match test reserved cpus")

		rootFxt.Log.Info("daemonset configuration", "reservedCPUs", driverConfig.ReservedCPUs.String(), "deviceMode", cpuDeviceMode, "groupBy", groupBy)

		_, targetNodeCPUInfo = mustDiscoverTargetNodeCPUInfo(ctx, rootFxt, infraFxt, dracpuTesterImage)

		allocatableCPUs := makeCPUSetFromDiscoveredCPUInfo(targetNodeCPUInfo)
		availableCPUs = allocatableCPUs.Difference(reservedCPUs)
		if reservedCPUs.Size() > 0 {
			gomega.Expect(availableCPUs).To(cpusetmatchers.HaveNoOverlapWith(reservedCPUs))
		}
		rootFxt.Log.Info("checking worker node", "coreCount", len(targetNodeCPUInfo.CPUs), "allocatableCPUs", allocatableCPUs.String(), "reservedCPUs", reservedCPUs.String(), "availableCPUs", availableCPUs.String())
	})

	ginkgo.When("sharing a CPU claim", func() {
		var fxt *fixture.Fixture
		var claim *resourcev1.ResourceClaim

		ginkgo.BeforeEach(func(ctx context.Context) {
			if cpuDeviceMode == "grouped" && groupBy == "machine" {
				ginkgo.Skip("skipping this test in machine grouping mode as we do not configure opaque config in claim")
			}
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
			if fxt != nil {
				gomega.Expect(fxt.Teardown(ctx)).To(gomega.Succeed())
			}
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
							ResourceClaimName: new(claim.Name),
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
			EventuallyFailedToCreate(ctx, fxt, createdPod2)
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
							ResourceClaimName: new(claim.Name),
						},
					},
				},
			}

			fixture.By("creating a pod with multiple containers consuming the ResourceClaim on %q, ensuring it gets ContainerCreateError", fxt.Namespace.Name)
			createdPod, err := fxt.K8SClientset.CoreV1().Pods(testPod.Namespace).Create(ctx, &testPod, metav1.CreateOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			EventuallyFailedToCreate(ctx, fxt, createdPod)
		})
	})
})
