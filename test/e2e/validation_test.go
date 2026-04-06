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

package e2e

import (
	"context"
	"os"
	"time"

	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	e2epod "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/pod"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

var _ = ginkgo.Describe("CPU request validation", ginkgo.Serial, ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		rootFxt           *fixture.Fixture
		dracpuTesterImage string
		cpuDeviceMode     string
	)

	ginkgo.BeforeAll(func(ctx context.Context) {
		dracpuTesterImage = os.Getenv("DRACPU_E2E_TEST_IMAGE")
		gomega.Expect(dracpuTesterImage).ToNot(gomega.BeEmpty(), "missing environment variable DRACPU_E2E_TEST_IMAGE")
		ginkgo.GinkgoLogr.Info("test image", "pullSpec", dracpuTesterImage)

		var err error
		rootFxt, err = fixture.ForGinkgo()
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create root fixture: %v", err)
		infraFxt := rootFxt.WithPrefix("infra")
		gomega.Expect(infraFxt.Setup(ctx)).To(gomega.Succeed())
		ginkgo.DeferCleanup(infraFxt.Teardown)

		ginkgo.By("checking the daemonset configuration")
		daemonSet, err := rootFxt.K8SClientset.AppsV1().DaemonSets("kube-system").Get(ctx, "dracpu", metav1.GetOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get dracpu daemonset")
		gomega.Expect(daemonSet.Spec.Template.Spec.Containers).ToNot(gomega.BeEmpty(), "no containers in dracpu daemonset")
		for _, arg := range daemonSet.Spec.Template.Spec.Containers[0].Args {
			if val, ok := parseCPUDeviceModeArg(arg); ok {
				cpuDeviceMode = val
			}
		}
		ginkgo.GinkgoLogr.Info("cpu device mode", "value", cpuDeviceMode)
	})

	ginkgo.Context("with matching CPU requests", func() {
		ginkgo.It("should successfully create container", func(ctx context.Context) {
			fxt := rootFxt.WithPrefix("matching-request")
			gomega.Expect(fxt.Setup(ctx)).To(gomega.Succeed())
			ginkgo.DeferCleanup(fxt.Teardown)

			claimCPUs := int64(1)
			containerCPUs := resource.MustParse("1")

			ginkgo.By("creating a ResourceClaim")
			claim := makeResourceClaim(fxt.Namespace.Name, "test-claim", claimCPUs, cpuDeviceMode)
			claim, err := fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Create(ctx, claim, metav1.CreateOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			ginkgo.By("creating a Pod with matching CPU request")
			pod := makePodWithClaim(fxt.Namespace.Name, "test-pod", claim.Name, &containerCPUs, nil)
			pod, err = fxt.K8SClientset.CoreV1().Pods(fxt.Namespace.Name).Create(ctx, pod, metav1.CreateOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			ginkgo.By("waiting for pod to be running")
			err = e2epod.WaitToBeRunning(ctx, fxt.K8SClientset, pod.Namespace, pod.Name)
			gomega.Expect(err).ToNot(gomega.HaveOccurred(), "pod should be running")
		})
	})

	ginkgo.Context("with mismatched CPU requests", func() {
		ginkgo.It("should fail to create container when request is too low", func(ctx context.Context) {
			fxt := rootFxt.WithPrefix("mismatch-low")
			gomega.Expect(fxt.Setup(ctx)).To(gomega.Succeed())
			ginkgo.DeferCleanup(fxt.Teardown)

			claimCPUs := int64(2)
			containerCPUs := resource.MustParse("1") // Mismatch: claim has 2, request is 1

			ginkgo.By("creating a ResourceClaim")
			claim := makeResourceClaim(fxt.Namespace.Name, "test-claim", claimCPUs, cpuDeviceMode)
			claim, err := fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Create(ctx, claim, metav1.CreateOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			ginkgo.By("creating a Pod with mismatched CPU request")
			pod := makePodWithClaim(fxt.Namespace.Name, "test-pod", claim.Name, &containerCPUs, nil)
			pod, err = fxt.K8SClientset.CoreV1().Pods(fxt.Namespace.Name).Create(ctx, pod, metav1.CreateOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			ginkgo.By("waiting for pod to fail with CreateContainerError")
			gomega.Eventually(ctx, func(ctx context.Context) (*v1.Pod, error) {
				return fxt.K8SClientset.CoreV1().Pods(fxt.Namespace.Name).Get(ctx, pod.Name, metav1.GetOptions{})
			}).
				WithTimeout(2*time.Minute).
				WithPolling(5*time.Second).
				Should(BeFailedToCreate(fxt), "pod should fail to create container")
		})

		ginkgo.It("should fail to create container when request is too high", func(ctx context.Context) {
			fxt := rootFxt.WithPrefix("mismatch-high")
			gomega.Expect(fxt.Setup(ctx)).To(gomega.Succeed())
			ginkgo.DeferCleanup(fxt.Teardown)

			claimCPUs := int64(1)
			containerCPUs := resource.MustParse("2") // Mismatch: claim has 1, request is 2

			ginkgo.By("creating a ResourceClaim")
			claim := makeResourceClaim(fxt.Namespace.Name, "test-claim", claimCPUs, cpuDeviceMode)
			claim, err := fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Create(ctx, claim, metav1.CreateOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			ginkgo.By("creating a Pod with mismatched CPU request")
			pod := makePodWithClaim(fxt.Namespace.Name, "test-pod", claim.Name, &containerCPUs, nil)
			pod, err = fxt.K8SClientset.CoreV1().Pods(fxt.Namespace.Name).Create(ctx, pod, metav1.CreateOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			ginkgo.By("waiting for pod to fail with CreateContainerError")
			gomega.Eventually(ctx, func(ctx context.Context) (*v1.Pod, error) {
				return fxt.K8SClientset.CoreV1().Pods(fxt.Namespace.Name).Get(ctx, pod.Name, metav1.GetOptions{})
			}).
				WithTimeout(2*time.Minute).
				WithPolling(5*time.Second).
				Should(BeFailedToCreate(fxt), "pod should fail to create container")
		})
	})

	ginkgo.Context("without CPU requests specified", func() {
		ginkgo.It("should successfully create container", func(ctx context.Context) {
			fxt := rootFxt.WithPrefix("no-request")
			gomega.Expect(fxt.Setup(ctx)).To(gomega.Succeed())
			ginkgo.DeferCleanup(fxt.Teardown)

			claimCPUs := int64(1)

			ginkgo.By("creating a ResourceClaim")
			claim := makeResourceClaim(fxt.Namespace.Name, "test-claim", claimCPUs, cpuDeviceMode)
			claim, err := fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Create(ctx, claim, metav1.CreateOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			ginkgo.By("creating a Pod without CPU request")
			pod := makePodWithClaim(fxt.Namespace.Name, "test-pod", claim.Name, nil, nil)
			pod, err = fxt.K8SClientset.CoreV1().Pods(fxt.Namespace.Name).Create(ctx, pod, metav1.CreateOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			ginkgo.By("waiting for pod to be running")
			err = e2epod.WaitToBeRunning(ctx, fxt.K8SClientset, pod.Namespace, pod.Name)
			gomega.Expect(err).ToNot(gomega.HaveOccurred(), "pod should be running")
		})
	})
})

// makeResourceClaim creates a ResourceClaim for the given number of CPUs
func makeResourceClaim(namespace, name string, cpuCount int64, deviceMode string) *resourcev1.ResourceClaim {
	claim := &resourcev1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: makeResourceClaimSpec(int(cpuCount), deviceMode == "grouped"),
	}
	return claim
}

// makePodWithClaim creates a Pod that references a ResourceClaim
func makePodWithClaim(namespace, name, claimName string, cpuRequest, cpuLimit *resource.Quantity) *v1.Pod {
	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: v1.PodSpec{
			RestartPolicy: v1.RestartPolicyNever,
			Containers: []v1.Container{
				{
					Name:  "test-container",
					Image: "registry.k8s.io/pause:3.9",
					Resources: v1.ResourceRequirements{
						Claims: []v1.ResourceClaim{
							{
								Name: "claim-ref",
							},
						},
					},
				},
			},
			ResourceClaims: []v1.PodResourceClaim{
				{
					Name:              "claim-ref",
					ResourceClaimName: ptr.To(claimName),
				},
			},
		},
	}

	// Set CPU request/limit if specified
	if cpuRequest != nil {
		pod.Spec.Containers[0].Resources.Requests = v1.ResourceList{
			v1.ResourceCPU: *cpuRequest,
		}
	}
	if cpuLimit != nil {
		pod.Spec.Containers[0].Resources.Limits = v1.ResourceList{
			v1.ResourceCPU: *cpuLimit,
		}
	}

	return pod
}
