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

	"github.com/kubernetes-sigs/dra-driver-cpu/internal/gatherinfo"
	e2eclient "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/client"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	e2epod "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/pod"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

var _ = ginkgo.Describe("dracpu-gatherinfo", ginkgo.Ordered, func() {
	var (
		fxt        *fixture.Fixture
		restConfig *rest.Config
	)

	ginkgo.BeforeAll(func() {
		var err error
		fxt = mustCreateFixture()
		restConfig, err = e2eclient.NewK8SConfig()
		skipIfMissingKubeconfig(err)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create Kubernetes config")
	})

	ginkgo.Context("when the driver DaemonSet is deployed and running", func() {
		ginkgo.It("should run from each driver pod", func(ctx context.Context) {
			pods := waitForRunningDriverPods(ctx, fxt.K8SClientset)

			for _, pod := range pods {
				ginkgo.By(fmt.Sprintf("running dracpu-gatherinfo on pod %s (node %s)",
					pod.Name, pod.Spec.NodeName))

				stdout, stderr, err := e2epod.Exec(ctx, restConfig, fxt.K8SClientset, &pod, "/dracpu-gatherinfo", "--stdout")
				gomega.Expect(err).NotTo(gomega.HaveOccurred(),
					"dracpu-gatherinfo failed in pod %q on node %q; stdout: %s; stderr: %s",
					pod.Name, pod.Spec.NodeName, stdout, stderr)

				var report gatherinfo.Report
				gomega.Expect(yaml.Unmarshal([]byte(stdout), &report)).To(gomega.Succeed(),
					"dracpu-gatherinfo output from pod %q should be valid YAML", pod.Name)
				gomega.Expect(report.LayoutVersion).To(gomega.Equal(gatherinfo.LayoutVersion),
					"dracpu-gatherinfo output from pod %q should include the report layout version", pod.Name)
				gomega.Expect(report.CPUDetails.CPUs).NotTo(gomega.BeEmpty(),
					"dracpu-gatherinfo output from pod %q should include CPU details", pod.Name)
				gomega.Expect(report.DriverConfig.CPUDeviceMode).NotTo(gomega.BeEmpty(),
					"dracpu-gatherinfo output from pod %q should include detected driver config", pod.Name)
			}
		})
	})
})
