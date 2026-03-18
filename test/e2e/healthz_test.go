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
	"io"
	"net/http"
	"time"

	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

const (
	daemonSetNamespace  = "kube-system"
	daemonSetLabel      = "app=dracpu"
	healthzPath         = "/healthz"
	driverHTTPPort      = 8080
	healthzPollInterval = 2 * time.Second
	healthzPollTimeout  = 2 * time.Minute
)

func listDriverPods(ctx context.Context, client kubernetes.Interface) ([]corev1.Pod, error) {
	podList, err := client.CoreV1().Pods(daemonSetNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: daemonSetLabel,
	})
	if err != nil {
		return nil, fmt.Errorf("listing pods with selector %q in %q: %w",
			daemonSetLabel, daemonSetNamespace, err)
	}
	return podList.Items, nil
}

func waitForPodIP(ctx context.Context, client kubernetes.Interface, podName string) (string, error) {
	var podIP string
	err := wait.PollUntilContextTimeout(ctx, healthzPollInterval, healthzPollTimeout, true,
		func(ctx context.Context) (bool, error) {
			pod, err := client.CoreV1().Pods(daemonSetNamespace).Get(ctx, podName, metav1.GetOptions{})
			if err != nil {
				return false, err
			}
			if pod.Status.PodIP == "" {
				return false, nil
			}
			podIP = pod.Status.PodIP
			return true, nil
		})
	if err != nil {
		return "", fmt.Errorf("waiting for PodIP on %q: %w", podName, err)
	}
	return podIP, nil
}

// healthzURL returns the full URL the test will GET, using the pod's IP
// directly rather than going through a Service, so each pod is probed individually.
func healthzURL(podIP string) string {
	return fmt.Sprintf("http://%s:%d%s", podIP, driverHTTPPort, healthzPath)
}

var _ = ginkgo.Describe("dra-driver-cpu HTTP health endpoints", ginkgo.Ordered, func() {
	var fxt *fixture.Fixture

	ginkgo.BeforeAll(func() {
		var err error
		fxt, err = fixture.ForGinkgo()
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create fixture")
	})

	// waitForRunningDriverPods gates both tests: it blocks until every pod
	// matched by the label selector has reached Running phase, then returns
	// the list so the individual tests can iterate over them.
	waitForRunningDriverPods := func(ctx context.Context) []corev1.Pod {
		ginkgo.GinkgoHelper()

		var pods []corev1.Pod
		gomega.Eventually(func(g gomega.Gomega) {
			var err error
			pods, err = listDriverPods(ctx, fxt.K8SClientset)
			g.Expect(err).NotTo(gomega.HaveOccurred())
			g.Expect(pods).NotTo(gomega.BeEmpty(),
				"no dra-driver-cpu pods found with selector %q in namespace %q",
				daemonSetLabel, daemonSetNamespace)
			for _, pod := range pods {
				g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning),
					"pod %q on node %q is not Running (phase=%s)",
					pod.Name, pod.Spec.NodeName, pod.Status.Phase)
			}
		}, healthzPollTimeout, healthzPollInterval).Should(gomega.Succeed(),
			"timed out waiting for dra-driver-cpu pods to reach Running phase")

		return pods
	}

	ginkgo.Context("when the driver DaemonSet is deployed and running", func() {

		// Test 1: verify the HTTP handler itself.
		// We hit /healthz directly on each pod IP and assert HTTP 200. The call
		// is wrapped in Eventually to tolerate the initialDelaySeconds window
		// (10 s liveness / 5 s readiness) during which Kubernetes hasn't started
		// probing yet either — so the server may still be initialising.
		ginkgo.It("should return HTTP 200 from /healthz on each driver pod", func(ctx context.Context) {
			pods := waitForRunningDriverPods(ctx)

			httpClient := &http.Client{Timeout: 10 * time.Second}

			for _, pod := range pods {
				podIP, err := waitForPodIP(ctx, fxt.K8SClientset, pod.Name)
				gomega.Expect(err).NotTo(gomega.HaveOccurred(),
					"obtaining PodIP for pod %q", pod.Name)

				url := healthzURL(podIP)
				ginkgo.By(fmt.Sprintf("GET %s (pod %s, node %s)", url, pod.Name, pod.Spec.NodeName))

				var lastStatus int
				var lastBody string
				gomega.Eventually(func(g gomega.Gomega) {
					resp, err := httpClient.Get(url) //nolint:noctx // httpClient.Timeout covers this
					g.Expect(err).NotTo(gomega.HaveOccurred(),
						"GET %s failed for pod %q on node %q", url, pod.Name, pod.Spec.NodeName)
					defer resp.Body.Close()

					bodyBytes, readErr := io.ReadAll(resp.Body)
					g.Expect(readErr).NotTo(gomega.HaveOccurred(), "reading response body")
					lastStatus = resp.StatusCode
					lastBody = string(bodyBytes)

					g.Expect(resp.StatusCode).To(gomega.Equal(http.StatusOK),
						"expected HTTP 200 from %s (pod %q, node %q), got %d; body: %s",
						url, pod.Name, pod.Spec.NodeName, resp.StatusCode, lastBody)
				}, healthzPollTimeout, healthzPollInterval).Should(gomega.Succeed(),
					"/healthz on %s did not return 200 within timeout (last status=%d, body=%q)",
					url, lastStatus, lastBody)
			}
		})

		// Test 2: verify the probe wiring in the YAML, not just the handler.
		// A container only becomes Ready after k8s itself has successfully
		// called the readiness probe (also /healthz:8080). So Ready=true means
		// the path, port, and delay values in the container spec are all correct.
		ginkgo.It("should mark every driver container as Ready (readiness probe passes)", func(ctx context.Context) {
			pods := waitForRunningDriverPods(ctx)

			for _, pod := range pods {
				ginkgo.By(fmt.Sprintf("checking Ready condition for pod %s (node %s)",
					pod.Name, pod.Spec.NodeName))

				gomega.Eventually(func(g gomega.Gomega) {
					current, err := fxt.K8SClientset.CoreV1().Pods(daemonSetNamespace).Get(ctx, pod.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())

					var anyReady bool
					for _, cs := range current.Status.ContainerStatuses {
						if cs.Ready {
							anyReady = true
							break
						}
					}
					g.Expect(anyReady).To(gomega.BeTrue(),
						"no container in pod %q (node %q) is Ready; statuses: %+v",
						pod.Name, pod.Spec.NodeName, current.Status.ContainerStatuses)
				}, healthzPollTimeout, healthzPollInterval).Should(gomega.Succeed(),
					"timed out waiting for a Ready container in pod %q", pod.Name)
			}
		})
	})
})
