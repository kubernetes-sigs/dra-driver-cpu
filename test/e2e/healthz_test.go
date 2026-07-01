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
	"strconv"
	"strings"

	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/net"
	"k8s.io/client-go/kubernetes"
)

const (
	healthzPath    = "/healthz"
	driverHTTPPort = 8080
)

func getPodHealthzViaAPIProxy(ctx context.Context, client kubernetes.Interface, pod v1.Pod) (int, string, error) {
	subPath := strings.TrimPrefix(healthzPath, "/")
	target := net.JoinSchemeNamePort("http", pod.Name, strconv.Itoa(driverHTTPPort))

	request := client.CoreV1().RESTClient().Get().
		Namespace(pod.Namespace).
		Resource("pods").
		SubResource("proxy").
		Name(target).
		Suffix(subPath)

	var statusCode int
	body, err := request.Do(ctx).StatusCode(&statusCode).Raw()
	if err != nil {
		return 0, "", err
	}
	return statusCode, string(body), nil
}

var _ = ginkgo.Describe("dra-driver-cpu HTTP health endpoints", ginkgo.Ordered, func() {
	var fxt *fixture.Fixture

	ginkgo.BeforeAll(func() {
		var err error
		fxt, err = fixture.ForGinkgo()
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create fixture")
	})

	ginkgo.Context("when the driver DaemonSet is deployed and running", func() {

		// Test 1: verify the HTTP handler itself.
		// We route the request through the apiserver pod proxy so the test does
		// not depend on the machine running `go test` being able to reach Pod IPs
		// directly.
		ginkgo.It("should return HTTP 200 from /healthz on each driver pod", func(ctx context.Context) {
			pods := waitForRunningDriverPods(ctx, fxt.K8SClientset)

			for _, pod := range pods {
				ginkgo.By(fmt.Sprintf("GET %s for pod %s (node %s) through the apiserver pod proxy",
					healthzPath, pod.Name, pod.Spec.NodeName))

				var lastStatus int
				var lastBody string
				gomega.Eventually(func(g gomega.Gomega) {
					statusCode, body, err := getPodHealthzViaAPIProxy(ctx, fxt.K8SClientset, pod)
					g.Expect(err).NotTo(gomega.HaveOccurred(),
						"GET %s through pod proxy failed for pod %q on node %q", healthzPath, pod.Name, pod.Spec.NodeName)
					lastStatus = statusCode
					lastBody = body

					g.Expect(statusCode).To(gomega.Equal(200),
						"expected HTTP 200 from %s via pod proxy (pod %q, node %q), got %d; body: %s",
						healthzPath, pod.Name, pod.Spec.NodeName, statusCode, lastBody)
				}, driverPodPollTimeout, driverPodPollInterval).Should(gomega.Succeed(),
					"%s via pod proxy for pod %q did not return 200 within timeout (last status=%d, body=%q)",
					healthzPath, pod.Name, lastStatus, lastBody)
			}
		})

		// Test 2: verify the probe wiring in the YAML, not just the handler.
		// A container only becomes Ready after k8s itself has successfully
		// called the readiness probe (also /healthz:8080). So Ready=true means
		// the path, port, and delay values in the container spec are all correct.
		ginkgo.It("should mark every driver container as Ready (readiness probe passes)", func(ctx context.Context) {
			pods := waitForRunningDriverPods(ctx, fxt.K8SClientset)

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
				}, driverPodPollTimeout, driverPodPollInterval).Should(gomega.Succeed(),
					"timed out waiting for a Ready container in pod %q", pod.Name)
			}
		})
	})
})
