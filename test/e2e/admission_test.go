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

	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var _ = ginkgo.Describe("Admission Webhook", ginkgo.Ordered, func() {
	var fxt *fixture.Fixture

	ginkgo.BeforeAll(func(ctx context.Context) {
		var err error
		fxt, err = fixture.ForGinkgo()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(fxt.Setup(ctx)).To(gomega.Succeed())
		ginkgo.DeferCleanup(func(ctx context.Context) {
			gomega.Expect(fxt.Teardown(ctx)).To(gomega.Succeed())
		})
		ensureAdmissionWebhookReady(ctx, fxt.K8SClientset)
	})

	ginkgo.It("allows a capacity claim that matches pod cpu requests", func(ctx context.Context) {
		claim := makeCapacityClaim(fxt.Namespace.Name, "claim-cpu-capacity-10", 10)
		_, err := fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Create(ctx, claim, metav1.CreateOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		pod := makePodWithClaim(fxt.Namespace.Name, "pod-cpu-dra-claim", "pod-cpu-claim-ref", claim.Name, 10)
		createdPod, err := fxt.K8SClientset.CoreV1().Pods(fxt.Namespace.Name).Create(ctx, pod, metav1.CreateOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		gomega.Expect(deletePod(ctx, fxt.K8SClientset, createdPod)).To(gomega.Succeed())
	})

	// allows multiple claims that match per-container cpu requests: pod with two containers, each with a claim and matching CPU request, is admitted.
	ginkgo.It("allows multiple claims that match per-container cpu requests", func(ctx context.Context) {
		claim4 := makeCountClaim(fxt.Namespace.Name, "cpu-request-4-cpus", 4)
		claim6 := makeCountClaim(fxt.Namespace.Name, "cpu-request-6-cpus", 6)
		_, err := fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Create(ctx, claim4, metav1.CreateOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		_, err = fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Create(ctx, claim6, metav1.CreateOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		pod := makePodWithTwoClaims(fxt.Namespace.Name, "pod-multi-claim",
			"container1-claim", claim4.Name, 4,
			"container2-claim", claim6.Name, 6,
		)
		createdPod, err := fxt.K8SClientset.CoreV1().Pods(fxt.Namespace.Name).Create(ctx, pod, metav1.CreateOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		gomega.Expect(deletePod(ctx, fxt.K8SClientset, createdPod)).To(gomega.Succeed())
	})

	// rejects pods where total CPU request does not match dra.cpu claim total (pod-level: must match exactly).
	ginkgo.It("rejects pods where request count does not match claim count", func(ctx context.Context) {
		claim := makeCountClaim(fxt.Namespace.Name, "cpu-claim-mismatch-4", 4)
		_, err := fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Create(ctx, claim, metav1.CreateOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		pod := makePodWithClaim(fxt.Namespace.Name, "pod-cpu-mismatch", "pod-cpu-claim-ref", claim.Name, 2)
		_, err = fxt.K8SClientset.CoreV1().Pods(fxt.Namespace.Name).Create(ctx, pod, metav1.CreateOptions{})
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(apierrors.IsInvalid(err)).To(gomega.BeTrue(), fmt.Sprintf("expected invalid error, got: %v", err))
	})

	// rejects pods where total CPU request exceeds dra.cpu claim total (must match exactly).
	ginkgo.It("rejects pods where cpu request exceeds claim count", func(ctx context.Context) {
		claim := makeCountClaim(fxt.Namespace.Name, "cpu-claim-excess-2", 2)
		_, err := fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Create(ctx, claim, metav1.CreateOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		pod := makePodWithClaim(fxt.Namespace.Name, "pod-cpu-excess", "pod-cpu-claim-ref", claim.Name, 4)
		_, err = fxt.K8SClientset.CoreV1().Pods(fxt.Namespace.Name).Create(ctx, pod, metav1.CreateOptions{})
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(apierrors.IsInvalid(err)).To(gomega.BeTrue(), fmt.Sprintf("expected invalid error, got: %v", err))
	})

	ginkgo.It("allows pods without resource claims", func(ctx context.Context) {
		pod := makePodWithoutClaim(fxt.Namespace.Name, "pod-no-claim", 1)
		createdPod, err := fxt.K8SClientset.CoreV1().Pods(fxt.Namespace.Name).Create(ctx, pod, metav1.CreateOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		gomega.Expect(deletePod(ctx, fxt.K8SClientset, createdPod)).To(gomega.Succeed())
	})

	// Pod-level validation requires total CPU request to match claim total; claim-only (no CPU request) is rejected.
	ginkgo.It("rejects claim-only pods without cpu requests", func(ctx context.Context) {
		claim := makeCountClaim(fxt.Namespace.Name, "claim-no-cpu", 1)
		_, err := fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Create(ctx, claim, metav1.CreateOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		pod := makePodWithClaimOnly(fxt.Namespace.Name, "pod-claim-no-cpu", "pod-claim-ref", claim.Name)
		_, err = fxt.K8SClientset.CoreV1().Pods(fxt.Namespace.Name).Create(ctx, pod, metav1.CreateOptions{})
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(apierrors.IsInvalid(err)).To(gomega.BeTrue(), fmt.Sprintf("expected invalid error, got: %v", err))
	})
})

func ensureAdmissionWebhookReady(ctx context.Context, cs kubernetes.Interface) {
	_, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		ginkgo.Skip(fmt.Sprintf("Cluster cannot be reached. Is a cluster attached for testing? In order to start kind, use make kind-cluster. Error: %v", err))
	}

	webhook, err := cs.AdmissionregistrationV1().ValidatingWebhookConfigurations().Get(ctx, "dracpu-admission", metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			ginkgo.Skip("dracpu-admission webhook not installed in cluster")
		}
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	}
	if len(webhook.Webhooks) == 0 || len(webhook.Webhooks[0].ClientConfig.CABundle) == 0 {
		ginkgo.Skip("dracpu-admission webhook is missing CA bundle")
	}

	endpointSlices, err := cs.DiscoveryV1().EndpointSlices("kube-system").List(ctx, metav1.ListOptions{
		LabelSelector: "kubernetes.io/service-name=dracpu-admission",
	})
	if err != nil {
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	}
	hasReadyEndpoint := false
	for _, slice := range endpointSlices.Items {
		if slice.AddressType != discoveryv1.AddressTypeIPv4 && slice.AddressType != discoveryv1.AddressTypeIPv6 {
			continue
		}
		for _, endpoint := range slice.Endpoints {
			if endpointReady(endpoint) {
				hasReadyEndpoint = true
				break
			}
		}
		if hasReadyEndpoint {
			break
		}
	}
	if !hasReadyEndpoint {
		ginkgo.Skip("dracpu-admission service has no ready endpoints")
	}
}

func endpointReady(endpoint discoveryv1.Endpoint) bool {
	if endpoint.Conditions.Ready != nil {
		return *endpoint.Conditions.Ready
	}
	return false
}

// makeCapacityClaim builds a dra.cpu capacity-based ResourceClaim.
func makeCapacityClaim(namespace, name string, cpu int64) *resourcev1.ResourceClaim {
	return &resourcev1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: resourcev1.ResourceClaimSpec{
			Devices: resourcev1.DeviceClaim{
				Requests: []resourcev1.DeviceRequest{
					{
						Name: "req-cpu-slice",
						Exactly: &resourcev1.ExactDeviceRequest{
							DeviceClassName: "dra.cpu",
							Capacity: &resourcev1.CapacityRequirements{
								Requests: map[resourcev1.QualifiedName]resource.Quantity{
									"dra.cpu/cpu": resource.MustParse(fmt.Sprintf("%d", cpu)),
								},
							},
						},
					},
				},
			},
		},
	}
}

// makeCountClaim builds a dra.cpu count-based ResourceClaim.
func makeCountClaim(namespace, name string, count int64) *resourcev1.ResourceClaim {
	return &resourcev1.ResourceClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: resourcev1.ResourceClaimSpec{
			Devices: resourcev1.DeviceClaim{
				Requests: []resourcev1.DeviceRequest{
					{
						Name: "req-cpu-count",
						Exactly: &resourcev1.ExactDeviceRequest{
							DeviceClassName: "dra.cpu",
							Count:           count,
						},
					},
				},
			},
		},
	}
}

// makePodWithClaim builds a pod that requests a CPU count and references a claim.
func makePodWithClaim(namespace, name, claimRefName, claimName string, cpu int64) *corev1.Pod {
	cpuQty := resource.MustParse(fmt.Sprintf("%d", cpu))
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "workload-container",
					Image: "registry.k8s.io/pause:3.9",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: cpuQty,
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU: cpuQty,
						},
						Claims: []corev1.ResourceClaim{
							{Name: claimRefName},
						},
					},
				},
			},
			ResourceClaims: []corev1.PodResourceClaim{
				{
					Name:              claimRefName,
					ResourceClaimName: &claimName,
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
}

// makePodWithTwoClaims builds a pod with two containers and claim references.
func makePodWithTwoClaims(namespace, name, claimRefName1, claimName1 string, cpu1 int64, claimRefName2, claimName2 string, cpu2 int64) *corev1.Pod {
	cpuQty1 := resource.MustParse(fmt.Sprintf("%d", cpu1))
	cpuQty2 := resource.MustParse(fmt.Sprintf("%d", cpu2))
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "container1",
					Image: "registry.k8s.io/pause:3.9",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: cpuQty1,
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU: cpuQty1,
						},
						Claims: []corev1.ResourceClaim{
							{Name: claimRefName1},
						},
					},
				},
				{
					Name:  "container2",
					Image: "registry.k8s.io/pause:3.9",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: cpuQty2,
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU: cpuQty2,
						},
						Claims: []corev1.ResourceClaim{
							{Name: claimRefName2},
						},
					},
				},
			},
			ResourceClaims: []corev1.PodResourceClaim{
				{
					Name:              claimRefName1,
					ResourceClaimName: &claimName1,
				},
				{
					Name:              claimRefName2,
					ResourceClaimName: &claimName2,
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
}

// makePodWithoutClaim builds a pod with CPU requests/limits and no claims.
func makePodWithoutClaim(namespace, name string, cpu int64) *corev1.Pod {
	cpuQty := resource.MustParse(fmt.Sprintf("%d", cpu))
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "workload-container",
					Image: "registry.k8s.io/pause:3.9",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: cpuQty,
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU: cpuQty,
						},
					},
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
}

// makePodWithClaimOnly builds a pod referencing a claim without cpu requests.
func makePodWithClaimOnly(namespace, name, claimRefName, claimName string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "workload-container",
					Image: "registry.k8s.io/pause:3.9",
					Resources: corev1.ResourceRequirements{
						Claims: []corev1.ResourceClaim{
							{Name: claimRefName},
						},
					},
				},
			},
			ResourceClaims: []corev1.PodResourceClaim{
				{
					Name:              claimRefName,
					ResourceClaimName: &claimName,
				},
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}
}

// deletePod removes a pod and ignores not-found errors.
func deletePod(ctx context.Context, cs kubernetes.Interface, pod *corev1.Pod) error {
	if pod == nil {
		return nil
	}
	err := cs.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	return nil
}
