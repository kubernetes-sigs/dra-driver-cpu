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
	"time"

	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/node"
	e2epod "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/pod"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ReportAfterSuite runs after all specs in the suite have completed,
// giving us access to the driver logs produced by the real e2e workload.
// This lets us validate contextual logging invariants (batchID consistency,
// claimUID presence, etc.) against actual log output.
// The alternative would be using a dedicated spec that must be ordered last,
// which seems more fragile and cumbersome.
var _ = ginkgo.ReportAfterSuite("contextual logging", func(report ginkgo.Report) {
	if !report.SuiteSucceeded {
		return
	}

	ctx := context.Background()

	rootFxt, err := fixture.ForGinkgo()
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create root fixture: %v", err)
	ctxlogFxt := rootFxt.WithPrefix("ctxlog")
	gomega.Expect(ctxlogFxt.Setup(ctx)).To(gomega.Succeed())
	defer func() { gomega.Expect(ctxlogFxt.Teardown(ctx)).To(gomega.Succeed()) }()

	var targetNode *v1.Node
	if targetNodeName := os.Getenv("DRACPU_E2E_TARGET_NODE"); len(targetNodeName) > 0 {
		targetNode, err = ctxlogFxt.K8SClientset.CoreV1().Nodes().Get(ctx, targetNodeName, metav1.GetOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get worker node %q: %v", targetNodeName, err)
	} else {
		gomega.Eventually(func() error {
			workerNodes, err := node.FindWorkers(ctx, ctxlogFxt.K8SClientset)
			if err != nil {
				return err
			}
			if len(workerNodes) == 0 {
				return fmt.Errorf("no worker nodes detected")
			}
			targetNode = workerNodes[0] // pick random one, this is the simplest random pick
			return nil
		}).WithTimeout(1*time.Minute).WithPolling(5*time.Second).Should(gomega.Succeed(), "failed to find any worker node")
	}
	ctxlogFxt.Log.Info("using worker node", "nodeName", targetNode.Name)

	dracpuPod, err := e2epod.GetDRACPUPod(ctx, ctxlogFxt.K8SClientset, targetNode.Name)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	logs, err := e2epod.GetLogs(ctxlogFxt.K8SClientset, ctx, dracpuPod.Namespace, dracpuPod.Name, dracpuPod.Spec.Containers[0].Name)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get logs from the dracpu pod: %v", err)

	// TODO: validate contextual logging invariants
	fmt.Fprintf(ginkgo.GinkgoWriter, "logs:\n%s", logs)
})
