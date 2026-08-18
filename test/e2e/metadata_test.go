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
	"os"
	"time"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
	e2eclient "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/client"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	e2enode "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/node"
	e2epod "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/pod"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
)

type metadataEntry struct {
	Path    string          `json:"path"`
	Content json.RawMessage `json:"content"`
}

type metadataFile struct {
	Requests []metadataRequest `json:"requests"`
}

type metadataRequest struct {
	Name    string           `json:"name"`
	Devices []metadataDevice `json:"devices"`
}

type metadataDevice struct {
	Driver     string                  `json:"driver"`
	Pool       string                  `json:"pool"`
	Name       string                  `json:"name"`
	Attributes map[string]metadataAttr `json:"attributes"`
}

type metadataAttr struct {
	Int    *int64  `json:"int,omitempty"`
	Bool   *bool   `json:"bool,omitempty"`
	String *string `json:"string,omitempty"`
}

var _ = ginkgo.Describe("Device Metadata", ginkgo.Ordered, func() {
	var (
		rootFxt           *fixture.Fixture
		restConfig        *rest.Config
		targetNode        *v1.Node
		dracpuTesterImage string
		cpuDeviceMode     string
		groupBy           string
		nodeAllocMapping  bool
	)

	ginkgo.BeforeAll(func(ctx context.Context) {
		dracpuTesterImage = os.Getenv("DRACPU_E2E_TEST_IMAGE")
		gomega.Expect(dracpuTesterImage).ToNot(gomega.BeEmpty(), "missing environment variable DRACPU_E2E_TEST_IMAGE")

		var err error
		rootFxt, err = fixture.ForGinkgo()
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create root fixture")

		restConfig, err = e2eclient.NewK8SConfig()
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create Kubernetes config")

		daemonSet, err := rootFxt.K8SClientset.AppsV1().DaemonSets("kube-system").Get(ctx, "dracpu", metav1.GetOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get dracpu daemonset")
		gomega.Expect(daemonSet.Spec.Template.Spec.Containers).ToNot(gomega.BeEmpty())
		cfgValues, err := getDriverConfigValues(ctx, rootFxt.K8SClientset, "kube-system", daemonSet)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot read driver config values")
		cpuDeviceMode = cfgValues.CPUDeviceMode
		groupBy = cfgValues.GroupBy
		nodeAllocMapping = cfgValues.PublishNodeAllocatableResourceMapping
		rootFxt.Log.Info("daemonset configuration", "mode", cpuDeviceMode, "groupBy", groupBy)

		targetNode, err = e2enode.PickWorker(ctx, rootFxt.K8SClientset, 5*time.Second, 1*time.Minute, rootFxt.Log)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		rootFxt.Log.Info("using worker node", "nodeName", targetNode.Name)
	})

	ginkgo.Context("when a pod with a CPU claim is running", func() {
		var fxt *fixture.Fixture

		ginkgo.BeforeEach(func(ctx context.Context) {
			fxt = rootFxt.WithPrefix("metadata")
			gomega.Expect(fxt.Setup(ctx)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func(ctx context.Context) {
			gomega.Expect(fxt.Teardown(ctx)).To(gomega.Succeed())
		})

		ginkgo.It("should publish device attributes as KEP-5304 metadata files", func(ctx context.Context) {
			if groupBy == device.GROUP_BY_MACHINE {
				ginkgo.Skip("skipping this test in machine grouping mode as we do not configure opaque config in claim")
			}

			const numCPUs = 2
			isConsumable := cpuDeviceMode != device.CPU_DEVICE_MODE_INDIVIDUAL

			ginkgo.By("creating a resource claim")
			claimName := "metadata-test-claim"
			claim := &resourcev1.ResourceClaim{
				ObjectMeta: metav1.ObjectMeta{
					Namespace: fxt.Namespace.Name,
					Name:      claimName,
				},
				Spec: makeResourceClaimSpec(numCPUs, isConsumable),
			}
			_, err := fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Create(ctx, claim, metav1.CreateOptions{})
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			ginkgo.By("creating a pod that references the claim")
			pod := makeTesterPodWithNamedClaim(fxt.Namespace.Name, dracpuTesterImage, claimName, targetNode.Name, nodeAllocMapping)
			pod, err = e2epod.CreateSync(ctx, fxt.K8SClientset, pod)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())

			ginkgo.By("reading metadata files from inside the container")
			stdout, stderr, err := e2epod.Exec(ctx, restConfig, fxt.K8SClientset, pod, "/dracpumetadata")
			gomega.Expect(err).ToNot(gomega.HaveOccurred(),
				"dracpumetadata failed; stdout: %s; stderr: %s", stdout, stderr)

			var entries []metadataEntry
			gomega.Expect(json.Unmarshal([]byte(stdout), &entries)).To(gomega.Succeed(),
				"failed to parse dracpumetadata output: %s", stdout)
			gomega.Expect(entries).ToNot(gomega.BeEmpty(), "no metadata files found")

			ginkgo.By("verifying metadata content")
			var metadata metadataFile
			gomega.Expect(json.Unmarshal(entries[0].Content, &metadata)).To(gomega.Succeed(),
				"failed to parse metadata JSON: %s", string(entries[0].Content))

			gomega.Expect(metadata.Requests).To(gomega.HaveLen(1))
			req := metadata.Requests[0]
			gomega.Expect(req.Name).To(gomega.Equal("request-cpus"))
			gomega.Expect(req.Devices).ToNot(gomega.BeEmpty())

			dev := req.Devices[0]
			gomega.Expect(dev.Driver).To(gomega.Equal("dra.cpu"))

			ginkgo.By("verifying mode-specific attributes")
			switch cpuDeviceMode {
			case device.CPU_DEVICE_MODE_INDIVIDUAL:
				expectIntAttr(dev.Attributes, "dra.cpu/cpuID")
				expectIntAttr(dev.Attributes, "dra.cpu/coreID")
				expectIntAttr(dev.Attributes, "dra.cpu/socketID")
				expectIntAttr(dev.Attributes, string(deviceattribute.StandardDeviceAttributeNUMANode))
				expectIntAttr(dev.Attributes, "dra.cpu/numaNodeID")
				expectIntAttr(dev.Attributes, "dra.net/numaNode")
				expectIntAttr(dev.Attributes, "dra.cpu/cacheL3ID")
				expectStringAttr(dev.Attributes, "dra.cpu/coreType")
				expectBoolAttr(dev.Attributes, "dra.cpu/smtEnabled")
			default: // grouped
				expectBoolAttr(dev.Attributes, "dra.cpu/smtEnabled")
				expectIntAttr(dev.Attributes, "dra.cpu/numCPUs")
				expectAllocatedNumCPUs(dev.Attributes, numCPUs)
				switch groupBy {
				case device.GROUP_BY_SOCKET:
					expectIntAttr(dev.Attributes, "dra.cpu/socketID")
				default: // NUMA node (default when groupBy is "" or "numanode")
					expectIntAttr(dev.Attributes, string(deviceattribute.StandardDeviceAttributeNUMANode))
					expectIntAttr(dev.Attributes, "dra.cpu/numaNodeID")
					expectIntAttr(dev.Attributes, "dra.net/numaNode")
					expectIntAttr(dev.Attributes, "dra.cpu/socketID")
				}
			}
		})
	})
})

func expectIntAttr(attrs map[string]metadataAttr, name string) {
	ginkgo.GinkgoHelper()
	attr, ok := attrs[name]
	gomega.Expect(ok).To(gomega.BeTrue(), "missing attribute %s", name)
	gomega.Expect(attr.Int).ToNot(gomega.BeNil(), "attribute %s should be int", name)
}

func expectBoolAttr(attrs map[string]metadataAttr, name string) {
	ginkgo.GinkgoHelper()
	attr, ok := attrs[name]
	gomega.Expect(ok).To(gomega.BeTrue(), "missing attribute %s", name)
	gomega.Expect(attr.Bool).ToNot(gomega.BeNil(), "attribute %s should be bool", name)
}

func expectStringAttr(attrs map[string]metadataAttr, name string) {
	ginkgo.GinkgoHelper()
	attr, ok := attrs[name]
	gomega.Expect(ok).To(gomega.BeTrue(), "missing attribute %s", name)
	gomega.Expect(attr.String).ToNot(gomega.BeNil(), "attribute %s should be string", name)
}

func expectAllocatedNumCPUs(attrs map[string]metadataAttr, expected int) {
	ginkgo.GinkgoHelper()
	attr, ok := attrs["dra.cpu/allocatedNumCPUs"]
	gomega.Expect(ok).To(gomega.BeTrue(), "missing dra.cpu/allocatedNumCPUs")
	gomega.Expect(attr.Int).ToNot(gomega.BeNil())
	gomega.Expect(*attr.Int).To(gomega.Equal(int64(expected)))

	numCPUsAttr, ok := attrs["dra.cpu/numCPUs"]
	gomega.Expect(ok).To(gomega.BeTrue(), "missing dra.cpu/numCPUs")
	gomega.Expect(*numCPUsAttr.Int).To(gomega.BeNumerically(">=", int64(expected)))
}
