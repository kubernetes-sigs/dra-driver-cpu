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

	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	healthzPath    = "/healthz"
	driverHTTPPort = 8080
)

var _ = ginkgo.Describe("dra-driver-cpu HTTP health endpoints", ginkgo.Ordered, func() {
	var fxt *fixture.Fixture

	ginkgo.BeforeAll(func() {
		var err error
		fxt, err = fixture.ForGinkgo()
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create fixture")
	})

	ginkgo.Context("when the driver DaemonSet is deployed and running", func() {

		// Test 1: verify the HTTP handler itself.
		// Use the Kubernetes pod proxy so this works when the test host cannot route
		// directly to the cluster's pod CIDR, as with Kind on Docker Desktop for macOS.
		// The proxy still targets each pod's real /healthz endpoint individually.
		ginkgo.It("should return HTTP 200 from /healthz on each driver pod", func(ctx context.Context) {
			pods := waitForRunningDriverPods(ctx, fxt.K8SClientset)

			for _, pod := range pods {
				ginkgo.By(fmt.Sprintf("GET %s through the pod proxy (pod %s, node %s)",
					healthzPath, pod.Name, pod.Spec.NodeName))

				gomega.Eventually(func(g gomega.Gomega) {
					body, err := fxt.K8SClientset.CoreV1().Pods(daemonSetNamespace).
						ProxyGet("http", pod.Name, strconv.Itoa(driverHTTPPort), healthzPath, nil).
						DoRaw(ctx)
					g.Expect(err).NotTo(gomega.HaveOccurred(),
						"GET %s through the pod proxy failed for pod %q on node %q; response body: %q",
						healthzPath, pod.Name, pod.Spec.NodeName, body)
				}, driverPodPollTimeout, driverPodPollInterval).Should(gomega.Succeed(),
					"%s through the pod proxy did not return HTTP 200 for pod %q within timeout",
					healthzPath, pod.Name)
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
