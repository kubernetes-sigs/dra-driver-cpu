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
	"strconv"

	"github.com/kubernetes-sigs/dra-driver-cpu/internal/gatherinfo"
	e2eclient "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/client"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	e2epod "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/pod"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

const (
	expectedNumCPUsEnv   = "DRACPU_E2E_EXPECTED_NUM_CPUS"
	expectedNUMANodesEnv = "DRACPU_E2E_EXPECTED_NUMA_NODES"
)

var _ = ginkgo.Describe("sysfs overlay", ginkgo.Ordered, func() {
	var (
		fxt                   *fixture.Fixture
		restConfig            *rest.Config
		overlayPath           string
		expectedNumCPUs       int
		expectedNUMANodeCount int
	)

	ginkgo.BeforeAll(func(ctx context.Context) {
		numCPUsValue := os.Getenv(expectedNumCPUsEnv)
		numaNodesValue := os.Getenv(expectedNUMANodesEnv)
		if numCPUsValue == "" && numaNodesValue == "" {
			ginkgo.Skip("sysfs overlay topology expectations are not configured")
		}
		gomega.Expect(numCPUsValue).NotTo(gomega.BeEmpty(), "both overlay topology expectation variables must be set")
		gomega.Expect(numaNodesValue).NotTo(gomega.BeEmpty(), "both overlay topology expectation variables must be set")

		var err error
		expectedNumCPUs, err = strconv.Atoi(numCPUsValue)
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s must be an integer", expectedNumCPUsEnv)
		gomega.Expect(expectedNumCPUs).To(gomega.BeNumerically(">", 0), "%s must be positive", expectedNumCPUsEnv)
		expectedNUMANodeCount, err = strconv.Atoi(numaNodesValue)
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s must be an integer", expectedNUMANodesEnv)
		gomega.Expect(expectedNUMANodeCount).To(gomega.BeNumerically(">", 0), "%s must be positive", expectedNUMANodesEnv)

		fxt, err = fixture.ForGinkgo()
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create fixture")

		daemonSet, err := fxt.K8SClientset.AppsV1().DaemonSets(daemonSetNamespace).
			Get(ctx, "dracpu", metav1.GetOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get dracpu daemonset")
		gomega.Expect(daemonSet.Spec.Template.Spec.Containers).NotTo(gomega.BeEmpty())
		overlayPath, _ = findArgInContainer(&daemonSet.Spec.Template.Spec.Containers[0], argSysFSOverlay)
		gomega.Expect(overlayPath).NotTo(gomega.BeEmpty(),
			"overlay topology expectations are set, but the driver has no %s argument", argSysFSOverlay)

		restConfig, err = e2eclient.NewK8SConfig()
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create Kubernetes config")
	})

	ginkgo.It("should expose the expected topology through each driver pod", func(ctx context.Context) {
		for _, pod := range waitForRunningDriverPods(ctx, fxt.K8SClientset) {
			readyPod := waitForReadyDriverPod(ctx, fxt.K8SClientset, pod.Spec.NodeName)
			stdout, stderr, err := e2epod.Exec(ctx, restConfig, fxt.K8SClientset, readyPod, "/dracpu-gatherinfo", "--stdout")
			gomega.Expect(err).NotTo(gomega.HaveOccurred(),
				"dracpu-gatherinfo failed in pod %q on node %q; stdout: %s; stderr: %s",
				readyPod.Name, readyPod.Spec.NodeName, stdout, stderr)

			var report gatherinfo.Report
			gomega.Expect(yaml.Unmarshal([]byte(stdout), &report)).To(gomega.Succeed(),
				"dracpu-gatherinfo output from pod %q should be valid YAML", readyPod.Name)
			gomega.Expect(report.DriverConfig.SysFSOverlay).To(gomega.Equal(overlayPath))
			gomega.Expect(report.CPUDetails.Topology.NumCPUs).To(gomega.Equal(expectedNumCPUs))
			gomega.Expect(report.CPUDetails.Topology.NumNUMANodes).To(gomega.Equal(expectedNUMANodeCount))
			gomega.Expect(report.CPUDetails.CPUs).To(gomega.HaveLen(expectedNumCPUs))

			cpuIDs := map[int]struct{}{}
			numaNodeIDs := map[int]struct{}{}
			for _, cpu := range report.CPUDetails.CPUs {
				_, duplicate := cpuIDs[cpu.CPUID]
				gomega.Expect(duplicate).To(gomega.BeFalse(), "CPU %d was reported more than once", cpu.CPUID)
				cpuIDs[cpu.CPUID] = struct{}{}
				numaNodeIDs[cpu.NUMANodeID] = struct{}{}
				gomega.Expect(cpu.NUMANodeCPUSet).NotTo(gomega.BeEmpty(), "CPU %d has no NUMA CPU set", cpu.CPUID)
			}
			gomega.Expect(numaNodeIDs).To(gomega.HaveLen(expectedNUMANodeCount))
		}
	})
})
