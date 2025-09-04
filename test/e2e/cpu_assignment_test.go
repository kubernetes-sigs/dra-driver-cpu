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
	"os"

	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/discovery"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/node"
	e2epod "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/pod"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/cpuset"
)

var _ = ginkgo.Describe("CPU Assignment", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var rootFxt *fixture.Fixture
	var targetNode *v1.Node
	var targetNodeCPUInfo discovery.DRACPUInfo
	var dracpuTesterImage string

	ginkgo.BeforeAll(func(ctx context.Context) {
		dracpuTesterImage = os.Getenv("DRACPU_TEST_IMAGE")
		gomega.Expect(dracpuTesterImage).ToNot(gomega.BeEmpty(), "missing environment variable DRACPU_TEST_IMAGE")
		ginkgo.GinkgoLogr.Info("discovery image", "pullSpec", dracpuTesterImage)

		var err error
		rootFxt, err = fixture.ForGinkgo()
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create root fixture: %v", err)
		infraFxt := rootFxt.WithPrefix("infra")
		gomega.Expect(infraFxt.Setup(ctx)).To(gomega.Succeed())
		ginkgo.DeferCleanup(infraFxt.Teardown, context.Background()) // TODO: set a timeout/reuse ctx?

		workerNodes, err := node.FindWorkers(ctx, infraFxt.K8SClientset)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot find worker nodes: %v", err)
		gomega.Expect(workerNodes).ToNot(gomega.BeEmpty(), "no worker nodes detected")

		targetNode = workerNodes[0] // pick random one, this is the simplest random pick
		ginkgo.GinkgoLogr.Info("using worker node", "nodeName", targetNode.Name)

		infoPod, err := e2epod.RunToCompletion(ctx, infraFxt.K8SClientset, makeDiscoveryPod(infraFxt.Namespace.Name, dracpuTesterImage))
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create discovery pod: %v", err)
		data, err := e2epod.GetLogs(infraFxt.K8SClientset, ctx, infoPod.Namespace, infoPod.Name, infoPod.Spec.Containers[0].Name)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get logs from discovery pod: %v", err)
		gomega.Expect(json.Unmarshal([]byte(data), &targetNodeCPUInfo)).To(gomega.Succeed())
		ginkgo.GinkgoLogr.Info("checking worker node", "coreCount", len(targetNodeCPUInfo.CPUs))

	})

	ginkgo.When("not setting resource claims", func() {
		var fxt *fixture.Fixture

		ginkgo.JustBeforeEach(func(ctx context.Context) {
			fxt = rootFxt.WithPrefix("no-res-claims")
			gomega.Expect(fxt.Setup(ctx)).To(gomega.Succeed())
		})

		ginkgo.JustAfterEach(func(ctx context.Context) {
			gomega.Expect(fxt.Teardown(ctx)).To(gomega.Succeed())
		})

		ginkgo.It("should grant best-effort pods access to all system CPUs", func(ctx context.Context) {
			pod, err := e2epod.CreateSync(ctx, fxt.K8SClientset, makeTesterPodBestEffort(fxt.Namespace.Name, dracpuTesterImage))
			gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create tester pod: %v", err)

			cpus, err := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, pod)
			gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get the CPUs allocated to tester pod %s/%s", pod.Namespace, pod.Name)
			gomega.Expect(cpus.Size()).To(gomega.Equal(len(targetNodeCPUInfo.CPUs)))
		})
	})
})

func getTesterPodCPUAllocation(cs kubernetes.Interface, ctx context.Context, pod *v1.Pod) (cpuset.CPUSet, error) {
	data, err := e2epod.GetLogs(cs, ctx, pod.Namespace, pod.Name, pod.Spec.Containers[0].Name)
	if err != nil {
		return cpuset.CPUSet{}, err
	}
	testerInfo := discovery.DRACPUTester{}
	err = json.Unmarshal([]byte(data), &testerInfo)
	if err != nil {
		return cpuset.CPUSet{}, err
	}
	return cpuset.Parse(testerInfo.Allocation.CPUs)
}

func makeTesterPodBestEffort(ns, image string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "tester-pod-",
			Namespace:    ns,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:    "tester-container",
					Image:   image,
					Command: []string{"/dracputester"},
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
