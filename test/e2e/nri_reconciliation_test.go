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
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/cpuset"
)

const (
	daemonSetNamespaceRule = "kube-system"
	pollIntervalRule       = 2 * time.Second
	pollTimeoutRule        = 2 * time.Minute
)

var _ = ginkgo.Describe("NRI Reconciliation on Restart", ginkgo.Serial, ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		rootFxt           *fixture.Fixture
		targetNode        *v1.Node
		targetNodeCPUInfo discovery.DRACPUInfo
		dracpuTesterImage string
		allocatableCPUs   cpuset.CPUSet
		reservedCPUs      cpuset.CPUSet
		cpuDeviceMode     string
		orgDaemonSet      *appsv1.DaemonSet
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
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create fixture")
		infraFxt := rootFxt.WithPrefix("infra")
		gomega.Expect(infraFxt.Setup(ctx)).To(gomega.Succeed())
		ginkgo.DeferCleanup(infraFxt.Teardown)

		ginkgo.By("getting the daemonset configuration")
		orgDaemonSet, err = rootFxt.K8SClientset.AppsV1().DaemonSets(daemonSetNamespaceRule).Get(ctx, "dracpu", metav1.GetOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get dracpu daemonset")
		gomega.Expect(orgDaemonSet.Spec.Template.Spec.Containers).ToNot(gomega.BeEmpty(), "no containers in dracpu daemonset")

		if val, ok := findArgInContainer(&orgDaemonSet.Spec.Template.Spec.Containers[0], argCPUDeviceMode); ok {
			cpuDeviceMode = val
		}

		// Find target node
		if targetNodeName := os.Getenv("DRACPU_E2E_TARGET_NODE"); len(targetNodeName) > 0 {
			targetNode, err = rootFxt.K8SClientset.CoreV1().Nodes().Get(ctx, targetNodeName, metav1.GetOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
		} else {
			gomega.Eventually(func() error {
				workerNodes, err := node.FindWorkers(ctx, infraFxt.K8SClientset)
				if err != nil {
					return err
				}
				if len(workerNodes) == 0 {
					return fmt.Errorf("no worker nodes detected")
				}
				targetNode = workerNodes[0]
				return nil
			}).WithTimeout(1 * time.Minute).WithPolling(5 * time.Second).Should(gomega.Succeed())
		}

		// Discover topology
		infoPod := discovery.MakePod(infraFxt.Namespace.Name, dracpuTesterImage)
		infoPod = e2epod.PinToNode(infoPod, targetNode.Name)
		infoPod, err = e2epod.RunToCompletion(ctx, infraFxt.K8SClientset, infoPod)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		data, err := e2epod.GetLogs(infraFxt.K8SClientset, ctx, infoPod.Namespace, infoPod.Name, infoPod.Spec.Containers[0].Name)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(json.Unmarshal([]byte(data), &targetNodeCPUInfo)).To(gomega.Succeed())
		allocatableCPUs = makeCPUSetFromDiscoveredCPUInfo(targetNodeCPUInfo)
	})

	ginkgo.It("should recover shared pool mask and preserve exclusive mask after restart", func(ctx context.Context) {
		fxt := rootFxt.WithPrefix("reconciliation")
		gomega.Expect(fxt.Setup(ctx)).To(gomega.Succeed())
		ginkgo.DeferCleanup(fxt.Teardown)

		ginkgo.By("Creating Pod 1 with exclusive CPUs")
		claimTemplate := resourcev1.ResourceClaimTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cpu-claim-reconcile-exclusive",
			},
			Spec: resourcev1.ResourceClaimTemplateSpec{
				Spec: makeResourceClaimSpec(2, cpuDeviceMode == "grouped"),
			},
		}
		createdClaimTemplate, err := fxt.K8SClientset.ResourceV1().ResourceClaimTemplates(fxt.Namespace.Name).Create(ctx, &claimTemplate, metav1.CreateOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		pod1 := makeTesterPodWithExclusiveCPUClaim(fxt.Namespace.Name, dracpuTesterImage, createdClaimTemplate.Name, 2, targetNode.Name)
		createdPod1, err := e2epod.CreateSync(ctx, fxt.K8SClientset, pod1)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		ginkgo.By("Verifying Pod 1 has correct exclusive CPU mask")
		alloc1 := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, createdPod1)
		fxt.Log.Info("Pod 1 CPU allocation", "cpuAssigned", alloc1.CPUAssigned.String())
		gomega.Expect(alloc1.CPUAssigned.Size()).To(gomega.Equal(2), "Pod 1 did not get exclusive CPUs")
		exclusiveCPUs := alloc1.CPUAssigned

		ginkgo.By("Stopping cpu dra driver pod on target node")
		// Defer restoration of DaemonSet
		ginkgo.DeferCleanup(func(ctx context.Context) {
			ginkgo.By("Restoring NRI plugin DaemonSet")
			ds, err := rootFxt.K8SClientset.AppsV1().DaemonSets(daemonSetNamespaceRule).Get(ctx, "dracpu", metav1.GetOptions{})
			if err != nil {
				return
			}
			ds.Spec = orgDaemonSet.Spec
			_, err = rootFxt.K8SClientset.AppsV1().DaemonSets(daemonSetNamespaceRule).Update(ctx, ds, metav1.UpdateOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
		})

		gomega.Eventually(func(g gomega.Gomega) {
			// Modify DaemonSet to exclude target node
			ds, err := rootFxt.K8SClientset.AppsV1().DaemonSets(daemonSetNamespaceRule).Get(ctx, "dracpu", metav1.GetOptions{})
			g.Expect(err).ToNot(gomega.HaveOccurred())

			req := v1.NodeSelectorRequirement{
				Key:      "kubernetes.io/hostname",
				Operator: v1.NodeSelectorOpNotIn,
				Values:   []string{targetNode.Name},
			}
			if ds.Spec.Template.Spec.Affinity == nil {
				ds.Spec.Template.Spec.Affinity = &v1.Affinity{}
			}
			if ds.Spec.Template.Spec.Affinity.NodeAffinity == nil {
				ds.Spec.Template.Spec.Affinity.NodeAffinity = &v1.NodeAffinity{}
			}
			if ds.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
				ds.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &v1.NodeSelector{}
			}
			terms := ds.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
			if len(terms) == 0 {
				ds.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = []v1.NodeSelectorTerm{
					{
						MatchExpressions: []v1.NodeSelectorRequirement{req},
					},
				}
			} else {
				for i := range terms {
					terms[i].MatchExpressions = append(terms[i].MatchExpressions, req)
				}
			}
			_, err = rootFxt.K8SClientset.AppsV1().DaemonSets(daemonSetNamespaceRule).Update(ctx, ds, metav1.UpdateOptions{})
			g.Expect(err).ToNot(gomega.HaveOccurred())
		}, pollTimeoutRule, pollIntervalRule).Should(gomega.Succeed(), "failed to update DaemonSet affinity")

		ginkgo.By("Waiting for cpu dra driver pod to terminate on target node")
		gomega.Eventually(func(g gomega.Gomega) {
			pods, err := listDriverPods(ctx, rootFxt.K8SClientset)
			g.Expect(err).NotTo(gomega.HaveOccurred())

			terminated := true
			for _, p := range pods {
				if p.Spec.NodeName == targetNode.Name && p.Status.Phase != v1.PodFailed && p.Status.Phase != v1.PodSucceeded {
					terminated = false
					break
				}
			}
			g.Expect(terminated).To(gomega.BeTrue(), "Pod on target node is still running")
		}, pollTimeoutRule, pollIntervalRule).Should(gomega.Succeed(), "timed out waiting for pod to terminate")

		ginkgo.By("Verifying Pod 1 still has correct exclusive CPU mask while NRI is down")
		alloc1Down := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, createdPod1)
		gomega.Expect(alloc1Down.CPUAssigned).To(gomega.Equal(exclusiveCPUs), "Pod 1 CPU mask changed after NRI stopped")

		ginkgo.By("Creating Pod 2 (Best Effort) on target node")
		pod2 := makeTesterPodBestEffort(fxt.Namespace.Name, dracpuTesterImage)
		pod2 = e2epod.PinToNode(pod2, targetNode.Name)
		createdPod2, err := e2epod.CreateSync(ctx, fxt.K8SClientset, pod2)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		ginkgo.By("Verifying Pod 2 CPU mask is NOT restricted to shared pool (should be default/all)")
		alloc2 := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, createdPod2)
		fxt.Log.Info("Pod 2 CPU allocation (without NRI)", "cpuAssigned", alloc2.CPUAssigned.String())
		// Since NRI is down, pod2 is not restricted to shared pool CPUs and gets all online CPUs.
		gomega.Expect(alloc2.CPUAffinity).To(gomega.Equal(allocatableCPUs), "Pod 2 CPU mask not equal to all CPUs")

		ginkgo.By("Bringing up the cpu dra driver on target node")
		// Restore original DaemonSet
		ds, err := rootFxt.K8SClientset.AppsV1().DaemonSets(daemonSetNamespaceRule).Get(ctx, "dracpu", metav1.GetOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		ds.Spec = orgDaemonSet.Spec
		_, err = rootFxt.K8SClientset.AppsV1().DaemonSets(daemonSetNamespaceRule).Update(ctx, ds, metav1.UpdateOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		ginkgo.By("Waiting for NRI plugin pod to become ready on target node")
		gomega.Eventually(func(g gomega.Gomega) {
			pods, err := listDriverPods(ctx, rootFxt.K8SClientset)
			g.Expect(err).NotTo(gomega.HaveOccurred())

			ready := false
			for _, p := range pods {
				if p.Spec.NodeName == targetNode.Name {
					for _, cs := range p.Status.ContainerStatuses {
						if cs.Ready {
							ready = true
							break
						}
					}
					if ready {
						break
					}
				}
			}
			g.Expect(ready).To(gomega.BeTrue(), "Pod on target node is not ready")
		}, pollTimeoutRule, pollIntervalRule).Should(gomega.Succeed(), "timed out waiting for pod to become ready")

		ginkgo.By("Verifying Pod 1 still has correct exclusive CPU mask after NRI restart")
		alloc1Up := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, createdPod1)
		gomega.Expect(alloc1Up.CPUAssigned).To(gomega.Equal(exclusiveCPUs), "Pod 1 CPU mask changed after NRI restarted")

		ginkgo.By("Verifying Pod 2 CPU mask IS restricted to shared pool (excludes Pod 1 CPUs)")
		gomega.Eventually(func(g gomega.Gomega) {
			alloc2Updated := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, createdPod2)
			fxt.Log.Info("Pod 2 CPU allocation (after NRI recovery)", "cpuAssigned", alloc2Updated.CPUAssigned.String())
			// pod2 must NOT contain the exclusive CPUs now
			g.Expect(alloc2Updated.CPUAssigned.Intersection(exclusiveCPUs).IsEmpty()).To(gomega.BeTrue(), "Pod 2 still has access to exclusive CPUs")
		}, pollTimeoutRule, pollIntervalRule).Should(gomega.Succeed(), "timed out waiting for Pod 2 CPU mask update")
	})
})
