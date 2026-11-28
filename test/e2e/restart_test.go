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
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/discovery"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	cpusetmatchers "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/matchers/cpuset"
	e2enode "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/node"
	e2epod "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/pod"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	v1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/cpuset"
)

// Serial keeps node-wide prepare counters and exclusive allocations isolated
// from other specs. This exercises workload restart, not driver restart.
var _ = ginkgo.Describe("Workload container restart", ginkgo.Serial, ginkgo.Label("restart"), func() {
	ginkgo.It("preserves exclusive CPUs without preparing the claim again", func(ctx context.Context) {
		image := os.Getenv("DRACPU_E2E_TEST_IMAGE")
		gomega.Expect(image).NotTo(gomega.BeEmpty(), "missing environment variable DRACPU_E2E_TEST_IMAGE")
		rootFxt, err := fixture.ForGinkgo()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fxt := rootFxt.WithPrefix("restart")
		gomega.Expect(fxt.Setup(ctx)).To(gomega.Succeed())
		ginkgo.DeferCleanup(fxt.Teardown)

		node, err := e2enode.PickWorker(ctx, fxt.K8SClientset, 5*time.Second, time.Minute, fxt.Log)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		cfg := getDriverConfig(ctx, fxt.K8SClientset)
		claimSpec := makeResourceClaimSpec(1, cfg.CPUDeviceMode == device.CPU_DEVICE_MODE_GROUPED)
		if cfg.CPUDeviceMode == device.CPU_DEVICE_MODE_GROUPED && cfg.GroupBy == device.GROUP_BY_MACHINE {
			reservedCPUs, err := cpuset.Parse(cfg.ReservedCPUs)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			availableCPUs, err := discoverAvailableCPUs(ctx, fxt, node.Name, image, reservedCPUs)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			if availableCPUs.IsEmpty() {
				ginkgo.Skip("no CPUs available for an exclusive claim")
			}
			claimSpec = makeResourceClaimSpecWithOpaqueConfig(1, true, cpuset.New(availableCPUs.List()[0]).String())
		}
		template, err := fxt.K8SClientset.ResourceV1().ResourceClaimTemplates(fxt.Namespace.Name).Create(ctx, &resourcev1.ResourceClaimTemplate{
			ObjectMeta: metav1.ObjectMeta{Name: "restart-exclusive"},
			Spec:       resourcev1.ResourceClaimTemplateSpec{Spec: claimSpec},
		}, metav1.CreateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("starting a workload with an exclusive CPU claim")
		pod := makeTesterPodWithExclusiveCPUClaim(fxt.Namespace.Name, image, template.Name, 1, node.Name, cfg.PublishNodeAllocatableResourceMapping)
		// Keep the workload out of PID 1: namespace init ignores signals sent by
		// peers unless it installed a handler. The ephemeral container can then
		// SIGKILL the workload without killing the pod sandbox.
		pod.Spec.ShareProcessNamespace = new(true)
		pod, err = e2epod.CreateSync(ctx, fxt.K8SClientset, pod)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		containerName := pod.Spec.Containers[0].Name
		var original v1.ContainerStatus
		gomega.Eventually(func(g gomega.Gomega) {
			current, err := fxt.K8SClientset.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(current.Status.ContainerStatuses).To(gomega.HaveLen(1))
			original = current.Status.ContainerStatuses[0]
			g.Expect(original.State.Running).NotTo(gomega.BeNil())
			g.Expect(original.ContainerID).NotTo(gomega.BeEmpty())
			g.Expect(original.Ready).To(gomega.BeTrue())
			expectRestartWorkloadReport(ctx, g, fxt, pod)
		}, 2*time.Minute, 2*time.Second).Should(gomega.Succeed())
		allocation := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, pod)
		gomega.Expect(allocation.CPUAssigned).To(cpusetmatchers.HaveSize(1))
		gomega.Expect(allocation.CPUAffinity).To(cpusetmatchers.Equal(allocation.CPUAssigned))
		gomega.Expect(allocation.CPUReported).To(cpusetmatchers.Equal(allocation.CPUAssigned))

		driverPod, err := restartDriverPod(ctx, fxt, node.Name)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(driverPod.Status.ContainerStatuses).NotTo(gomega.BeEmpty())
		for _, status := range driverPod.Status.ContainerStatuses {
			gomega.Expect(status.ContainerID).NotTo(gomega.BeEmpty())
			gomega.Expect(status.Ready).To(gomega.BeTrue())
		}
		rawMetrics, err := restartDriverMetrics(ctx, fxt, driverPod)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		prepares, err := restartPrepareCounters(rawMetrics)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(prepares["success"]).To(gomega.BeNumerically(">=", 1))

		ginkgo.By("killing only the workload process from an ephemeral container")
		current, err := fxt.K8SClientset.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		current.Spec.EphemeralContainers = append(current.Spec.EphemeralContainers, v1.EphemeralContainer{
			EphemeralContainerCommon: v1.EphemeralContainerCommon{
				Name:  "trigger-restart",
				Image: "registry.k8s.io/e2e-test-images/busybox:1.36.1-1",
				Command: []string{"sh", "-ec", `for p in /proc/[0-9]*/comm; do
 if [ "$(cat "$p" 2>/dev/null)" = dracputester ]; then
  pid=${p#/proc/}; pid=${pid%/comm}
  kill -KILL "$pid"
  exit 0
 fi
done
echo 'workload process not found' >&2
exit 1`},
				SecurityContext: &v1.SecurityContext{RunAsUser: new(int64(0)), Capabilities: &v1.Capabilities{Add: []v1.Capability{"KILL"}}},
			},
			TargetContainerName: containerName,
		})
		_, err = fxt.K8SClientset.CoreV1().Pods(pod.Namespace).UpdateEphemeralContainers(ctx, pod.Name, current, metav1.UpdateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		ginkgo.By("waiting for a replacement container in the same pod")
		gomega.Eventually(func(g gomega.Gomega) {
			current, err := fxt.K8SClientset.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(current.UID).To(gomega.Equal(pod.UID))
			g.Expect(current.Status.ResourceClaimStatuses).To(gomega.Equal(pod.Status.ResourceClaimStatuses))
			g.Expect(current.Status.EphemeralContainerStatuses).To(gomega.HaveLen(1))
			terminated := current.Status.EphemeralContainerStatuses[0].State.Terminated
			g.Expect(terminated).NotTo(gomega.BeNil())
			g.Expect(terminated.ExitCode).To(gomega.BeZero())
			g.Expect(current.Status.ContainerStatuses).To(gomega.HaveLen(1))
			replacement := current.Status.ContainerStatuses[0]
			g.Expect(replacement.Name).To(gomega.Equal(containerName))
			g.Expect(replacement.ContainerID).NotTo(gomega.BeEmpty())
			g.Expect(replacement.ContainerID).NotTo(gomega.Equal(original.ContainerID))
			g.Expect(replacement.RestartCount).To(gomega.Equal(original.RestartCount + 1))
			g.Expect(replacement.LastTerminationState.Terminated).NotTo(gomega.BeNil())
			g.Expect(replacement.LastTerminationState.Terminated.ExitCode).To(gomega.Equal(int32(137)))
			g.Expect(replacement.State.Running).NotTo(gomega.BeNil())
			g.Expect(replacement.Ready).To(gomega.BeTrue())
			expectRestartWorkloadReport(ctx, g, fxt, pod)
		}, 3*time.Minute, 2*time.Second).Should(gomega.Succeed())

		ginkgo.By("checking the replacement retained its exclusive cpuset and affinity")
		replacementAllocation := getTesterPodCPUAllocation(fxt.K8SClientset, ctx, pod)
		gomega.Expect(replacementAllocation.CPUAssigned).To(cpusetmatchers.Equal(allocation.CPUAssigned))
		gomega.Expect(replacementAllocation.CPUAffinity).To(cpusetmatchers.Equal(allocation.CPUAssigned))
		gomega.Expect(replacementAllocation.CPUReported).To(cpusetmatchers.Equal(allocation.CPUAssigned))

		ginkgo.By("checking no prepare call occurred and the driver did not restart")
		currentDriver, err := restartDriverPod(ctx, fxt, node.Name)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(currentDriver.UID).To(gomega.Equal(driverPod.UID))
		gomega.Expect(currentDriver.Status.ContainerStatuses).To(gomega.HaveLen(len(driverPod.Status.ContainerStatuses)))
		for i, status := range currentDriver.Status.ContainerStatuses {
			gomega.Expect(status.Name).To(gomega.Equal(driverPod.Status.ContainerStatuses[i].Name))
			gomega.Expect(status.ContainerID).To(gomega.Equal(driverPod.Status.ContainerStatuses[i].ContainerID))
			gomega.Expect(status.RestartCount).To(gomega.Equal(driverPod.Status.ContainerStatuses[i].RestartCount))
		}
		rawMetrics, err = restartDriverMetrics(ctx, fxt, driverPod)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		after, err := restartPrepareCounters(rawMetrics)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Expect(after).To(gomega.Equal(prepares), "workload restart must not prepare the claim again")
	})
})

func expectRestartWorkloadReport(ctx context.Context, g gomega.Gomega, fxt *fixture.Fixture, pod *v1.Pod) {
	ginkgo.GinkgoHelper()
	data, err := e2epod.GetLogs(ctx, fxt.K8SClientset, pod)
	g.Expect(err).NotTo(gomega.HaveOccurred())
	var report discovery.DRACPUTester
	g.Expect(unmarshalLatestReport(data, &report)).To(gomega.Succeed())
}

func restartPrepareCounters(raw string) (map[string]float64, error) {
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(raw))
	if err != nil {
		return nil, err
	}
	family, ok := families["dra_cpu_prepare_claims_total"]
	if !ok {
		return nil, fmt.Errorf("missing dra_cpu_prepare_claims_total")
	}
	counters := make(map[string]float64)
	for _, metric := range family.Metric {
		if metric.Counter == nil {
			return nil, fmt.Errorf("prepare metric is not a counter")
		}
		if len(metric.Label) != 1 || metric.Label[0].GetName() != "result" || metric.Label[0].GetValue() == "" {
			return nil, fmt.Errorf("prepare metric must have exactly one nonempty result label")
		}
		result := metric.Label[0].GetValue()
		if _, exists := counters[result]; exists {
			return nil, fmt.Errorf("duplicate prepare result %q", result)
		}
		counters[result] = metric.Counter.GetValue()
	}
	if len(counters) == 0 {
		return nil, fmt.Errorf("prepare metric has no result labels")
	}
	return counters, nil
}

// Use the configured driver namespace and apiserver proxy so the spec also
// works when the test runner cannot route directly to the node's pod network.
func restartDriverPod(ctx context.Context, fxt *fixture.Fixture, nodeName string) (*v1.Pod, error) {
	pods, err := listDriverPods(ctx, fxt.K8SClientset)
	if err != nil {
		return nil, err
	}
	var found *v1.Pod
	for _, pod := range pods {
		if pod.Spec.NodeName != nodeName || pod.DeletionTimestamp != nil {
			continue
		}
		if found != nil {
			return nil, fmt.Errorf("multiple driver pods on node %q", nodeName)
		}
		found = &pod
	}
	if found == nil {
		return nil, fmt.Errorf("no driver pod on node %q", nodeName)
	}
	return found, nil
}

func restartDriverMetrics(ctx context.Context, fxt *fixture.Fixture, pod *v1.Pod) (string, error) {
	data, err := fxt.K8SClientset.CoreV1().RESTClient().Get().
		Namespace(pod.Namespace).Resource("pods").
		Name(fmt.Sprintf("%s:%d", pod.Name, driverHTTPPort)).
		SubResource("proxy").Suffix(metricsPath).
		Timeout(10 * time.Second).Do(ctx).Raw()
	return string(data), err
}
