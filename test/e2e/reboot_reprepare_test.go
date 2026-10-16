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
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/cpuset"
)

const (
	// rebootPollInterval is the polling cadence shared by all the asynchronous
	// checks in this suite.
	rebootPollInterval = 2 * time.Second

	// rebootPollTimeout bounds a single asynchronous check. A node reboot on a
	// local cluster converges well within this budget; go test needs a larger
	// -timeout than its 10m default to turn a stuck suite into a clean failure
	// instead of a panic, which the Makefile provides.
	rebootPollTimeout = 3 * time.Minute

	// rebootConvergeGrace is how long the specs watch the driver for a second
	// prepare after a kubelet-only restart. Restarting the kubelet must not
	// re-prepare, so this is a negative check and the window is its strength.
	rebootConvergeGrace = 60 * time.Second

	prepareClaimsTotalMetric   = "dra_cpu_prepare_claims_total"
	unprepareClaimsTotalMetric = "dra_cpu_unprepare_claims_total"

	// rebootSpecLabel marks the specs which inject faults at the node level.
	// They need a cluster whose nodes run as containers on the test host.
	rebootSpecLabel = "node-fault"
)

// This suite complements the unit coverage with a real cluster check of the
// behavior kubelet relies on after a node reboot: the node's /run is a tmpfs, so
// the CDI specs on the hostPath /var/run/cdi are lost, and kubelet calls
// NodePrepareResources again. The driver must rebuild the CDI spec on the
// prepare path and hand the container the same CPUs, without recording errors.
//
// The specs act on the container backing the node, so they need a kind-like
// cluster; they skip themselves when that is not possible.
var _ = ginkgo.Describe("Reboot and reprepare", ginkgo.Label(rebootSpecLabel), ginkgo.Serial, ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		rootFxt                *fixture.Fixture
		targetNode             *v1.Node
		dracpuTesterImage      string
		cpuDeviceMode          string
		groupBy                string
		nodeAllocatableMapping bool
		injector               *e2enode.FaultInjector
		orgDaemonSet           *appsv1.DaemonSet
	)

	ginkgo.BeforeAll(func(ctx context.Context) {
		dracpuTesterImage = os.Getenv("DRACPU_E2E_TEST_IMAGE")
		gomega.Expect(dracpuTesterImage).ToNot(gomega.BeEmpty(), "missing environment variable DRACPU_E2E_TEST_IMAGE")

		var err error
		rootFxt, err = fixture.ForGinkgo()
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create fixture")

		infraFxt := rootFxt.WithPrefix("rebootinfra")
		gomega.Expect(infraFxt.Setup(ctx)).To(gomega.Succeed())
		ginkgo.DeferCleanup(infraFxt.Teardown)

		fixture.By("reading the driver configuration")
		daemonSet, err := rootFxt.K8SClientset.AppsV1().DaemonSets(daemonSetNamespaceRule).Get(ctx, driverDaemonSetName, metav1.GetOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get dracpu daemonset")
		orgDaemonSet = daemonSet
		cfgValues, err := getDriverConfigValues(ctx, rootFxt.K8SClientset, daemonSetNamespaceRule, daemonSet)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot read dracpu driver config values")
		cpuDeviceMode = cfgValues.CPUDeviceMode
		groupBy = cfgValues.GroupBy
		nodeAllocatableMapping = cfgValues.PublishNodeAllocatableResourceMapping
		rootFxt.Log.Info("driver configuration", "cpuDeviceMode", cpuDeviceMode, "groupBy", groupBy)

		targetNode, err = e2enode.PickWorker(ctx, rootFxt.K8SClientset, 5*time.Second, time.Minute, rootFxt.Log)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		rootFxt.Log.Info("using worker node", "nodeName", targetNode.Name)

		injector, err = e2enode.NewFaultInjector(ctx, targetNode.Name)
		if err != nil {
			ginkgo.Skip(fmt.Sprintf("node fault injection is not available for node %q: %v", targetNode.Name, err))
		}
	})

	ginkgo.BeforeEach(func(ctx context.Context) {
		if cpuDeviceMode == device.CPU_DEVICE_MODE_GROUPED && groupBy == device.GROUP_BY_MACHINE {
			ginkgo.Skip("skipping this test in machine grouping mode as we do not configure opaque config in claim")
		}
	})

	// createClaim creates a ResourceClaim asking for the given number of CPUs
	// and returns the created object.
	createClaim := func(ctx context.Context, fxt *fixture.Fixture, name string, cpus int) *resourcev1.ResourceClaim {
		ginkgo.GinkgoHelper()

		fixture.By("creating the ResourceClaim %q asking for %d CPUs", name, cpus)
		claim := &resourcev1.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: fxt.Namespace.Name,
				Name:      name,
			},
			Spec: makeResourceClaimSpec(cpus, cpuDeviceMode == device.CPU_DEVICE_MODE_GROUPED),
		}
		created, err := fxt.K8SClientset.ResourceV1().ResourceClaims(fxt.Namespace.Name).Create(ctx, claim, metav1.CreateOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		return created
	}

	// createTesterPod creates a tester pod bound to an already allocated claim.
	createTesterPod := func(ctx context.Context, fxt *fixture.Fixture, claim *resourcev1.ResourceClaim) *v1.Pod {
		ginkgo.GinkgoHelper()

		fixture.By("creating a tester pod using the claim")
		pod := makeTesterPodWithNamedClaim(fxt.Namespace.Name, dracpuTesterImage, targetNode.Name, nodeAllocatableMapping, claim)
		created, err := e2epod.CreateSync(ctx, fxt.K8SClientset, pod)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		return created
	}

	// expectAssignedCPUs waits until the tester pod reports the expected CPUs.
	expectAssignedCPUs := func(ctx context.Context, fxt *fixture.Fixture, pod *v1.Pod, want cpuset.CPUSet) {
		ginkgo.GinkgoHelper()

		gomega.Eventually(func(g gomega.Gomega) {
			got, err := rebootPodAssignedCPUs(ctx, fxt.K8SClientset, pod)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(got).To(gomega.Equal(want))
		}, rebootPollTimeout, rebootPollInterval).Should(gomega.Succeed(), "tester pod %s did not report CPUs %s", e2epod.Identify(pod), want.String())
	}

	// waitAssignedCPUs waits until the tester pod reports a non-empty CPU set
	// and returns it.
	waitAssignedCPUs := func(ctx context.Context, fxt *fixture.Fixture, pod *v1.Pod) cpuset.CPUSet {
		ginkgo.GinkgoHelper()

		var cpus cpuset.CPUSet
		gomega.Eventually(func(g gomega.Gomega) {
			got, err := rebootPodAssignedCPUs(ctx, fxt.K8SClientset, pod)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(got.Size()).To(gomega.BeNumerically(">", 0), "tester pod %s reported no CPUs", e2epod.Identify(pod))
			cpus = got
		}, rebootPollTimeout, rebootPollInterval).Should(gomega.Succeed())
		return cpus
	}

	// expectCounterSuccess waits until the given counter recorded at least
	// wantMin successful operations.
	expectCounterSuccess := func(ctx context.Context, metricName string, wantMin float64) {
		ginkgo.GinkgoHelper()

		gomega.Eventually(func(g gomega.Gomega) {
			values, err := rebootCounterValues(ctx, rootFxt, targetNode.Name, metricName)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(values["success"]).To(gomega.BeNumerically(">=", wantMin))
		}, rebootPollTimeout, rebootPollInterval).Should(gomega.Succeed(), "not enough successful %s operations", metricName)
	}

	// expectNoDriverErrorResults verifies that the driver recorded neither a
	// prepare nor an unprepare error.
	//
	// A counter exports no samples until it is first observed, so an absent
	// unprepare counter means nothing was unprepared, which is not an error. The
	// prepare counter is required instead: it anchors the check to an observed
	// prepare, so the assertion cannot pass on a failed or empty scrape.
	expectNoDriverErrorResults := func(ctx context.Context) {
		ginkgo.GinkgoHelper()

		prepare, err := rebootCounterValues(ctx, rootFxt, targetNode.Name, prepareClaimsTotalMetric)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(prepare).ToNot(gomega.BeEmpty(), "the driver did not report any prepare result")

		unprepare, err := rebootCounterValues(ctx, rootFxt, targetNode.Name, unprepareClaimsTotalMetric)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		gomega.Expect(prepare["error"]).To(gomega.BeZero(), "unexpected prepare errors")
		gomega.Expect(unprepare["error"]).To(gomega.BeZero(), "unexpected unprepare errors")
	}

	ginkgo.It("rebuilds the CDI spec and re-prepares the claim after a node reboot", func(ctx context.Context) {
		fxt := rootFxt.WithPrefix("nodereboot")
		gomega.Expect(fxt.Setup(ctx)).To(gomega.Succeed())
		ginkgo.DeferCleanup(fxt.Teardown)

		claim := createClaim(ctx, fxt, "claim-node-reboot", 2)
		createdPod := createTesterPod(ctx, fxt, claim)

		fixture.By("recording the CPUs assigned before the reboot")
		beforeCPUs := waitAssignedCPUs(ctx, fxt, createdPod)
		gomega.Expect(beforeCPUs).To(cpusetmatchers.HaveSize(2))
		fxt.Log.Info("pre-reboot allocation", "cpus", beforeCPUs.String(), "containerID", rebootContainerID(createdPod))

		expectCDISpecForClaim(ctx, injector, claim, true)

		fixture.By("rebooting node %q", targetNode.Name)
		rebootRequestedAt := time.Now()
		gomega.Expect(injector.Restart(ctx)).To(gomega.Succeed())

		// The reboot wipes the CDI spec directory and restarts the driver, so
		// waiting for the spec to be rewritten waits for both, without assuming
		// whether the runtime recreated or restored the pod containers.
		fixture.By("waiting for the driver to rewrite the CDI spec")
		expectCDISpecRebuiltAfter(ctx, injector, claim, rebootRequestedAt)

		fixture.By("waiting for the node and the driver to come back")
		expectNodeReady(ctx, rootFxt.K8SClientset, targetNode.Name)
		waitForDriverPodReadyOnNode(ctx, rootFxt.K8SClientset, targetNode.Name)

		fixture.By("verifying the container gets the same CPUs as before the reboot")
		expectAssignedCPUs(ctx, fxt, createdPod, beforeCPUs)

		fixture.By("verifying prepare succeeded again and no error was recorded")
		// The node reboot removes the CDI specs without a preceding unprepare,
		// so only the prepare counter is expected to move.
		expectCounterSuccess(ctx, prepareClaimsTotalMetric, 1)
		expectNoDriverErrorResults(ctx)

		fixture.By("verifying the allocated CPU gauges reflect the re-prepared claim")
		gomega.Eventually(func(g gomega.Gomega) {
			rawMetrics, err := getDriverRawMetrics(ctx, rootFxt, targetNode.Name)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			snapshot, err := parseAllocationMetricSnapshot(rawMetrics)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(snapshot.AllocatedCPUs).To(gomega.Equal(beforeCPUs.Size()))
			g.Expect(snapshot.ActiveResourceClaims).To(gomega.Equal(1))
		}, rebootPollTimeout, rebootPollInterval).Should(gomega.Succeed())
	})

	ginkgo.It("does not re-prepare the claim on a kubelet restart", func(ctx context.Context) {
		fxt := rootFxt.WithPrefix("kubeletrestart")
		gomega.Expect(fxt.Setup(ctx)).To(gomega.Succeed())
		ginkgo.DeferCleanup(fxt.Teardown)

		claim := createClaim(ctx, fxt, "claim-kubelet-restart", 2)
		createdPod := createTesterPod(ctx, fxt, claim)

		fixture.By("recording the allocation and the prepare count before the kubelet restart")
		beforeCPUs := waitAssignedCPUs(ctx, fxt, createdPod)
		gomega.Expect(beforeCPUs).To(cpusetmatchers.HaveSize(2))
		beforeContainerID := rebootContainerID(createdPod)
		gomega.Expect(beforeContainerID).ToNot(gomega.BeEmpty(), "tester pod has no container yet")

		expectCounterSuccess(ctx, prepareClaimsTotalMetric, 1)
		expectNoDriverErrorResults(ctx)
		beforePrepare, err := rebootCounterValues(ctx, rootFxt, targetNode.Name, prepareClaimsTotalMetric)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		beforePrepareSuccess := beforePrepare["success"]
		fxt.Log.Info("pre-restart state", "cpus", beforeCPUs.String(), "containerID", beforeContainerID, "prepareSuccess", beforePrepareSuccess)

		fixture.By("restarting the kubelet on node %q", targetNode.Name)
		gomega.Expect(injector.RestartKubelet(ctx)).To(gomega.Succeed())

		fixture.By("waiting for the kubelet to be active again")
		gomega.Eventually(func(g gomega.Gomega) {
			active, err := injector.KubeletActive(ctx)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(active).To(gomega.BeTrue())
		}, rebootPollTimeout, rebootPollInterval).Should(gomega.Succeed())

		fixture.By("verifying the driver was not asked to prepare the claim again")
		// The kubelet keeps the claim's devices and CDI IDs in its DRA manager
		// state, so a kubelet-only restart must not trigger a second prepare.
		gomega.Consistently(func(g gomega.Gomega) {
			values, err := rebootCounterValues(ctx, rootFxt, targetNode.Name, prepareClaimsTotalMetric)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(values["success"]).To(gomega.Equal(beforePrepareSuccess))
			g.Expect(values["error"]).To(gomega.BeZero())

			current, err := rebootFetchPod(ctx, fxt.K8SClientset, createdPod)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(rebootContainerID(current)).To(gomega.Equal(beforeContainerID), "container was recreated by a kubelet-only restart")
		}).WithTimeout(rebootConvergeGrace).WithPolling(rebootPollInterval).Should(gomega.Succeed())

		fixture.By("verifying the container kept the same CPUs")
		expectAssignedCPUs(ctx, fxt, createdPod, beforeCPUs)
	})

	ginkgo.It("keeps the same CPUs when the pod is deleted and recreated", func(ctx context.Context) {
		fxt := rootFxt.WithPrefix("podrecreate")
		gomega.Expect(fxt.Setup(ctx)).To(gomega.Succeed())
		ginkgo.DeferCleanup(fxt.Teardown)

		claim := createClaim(ctx, fxt, "claim-pod-recreate", 2)
		createdPod := createTesterPod(ctx, fxt, claim)

		fixture.By("recording the CPUs assigned to the first pod")
		beforeCPUs := waitAssignedCPUs(ctx, fxt, createdPod)
		gomega.Expect(beforeCPUs).To(cpusetmatchers.HaveSize(2))
		fxt.Log.Info("pre-recreate allocation", "cpus", beforeCPUs.String())

		expectCounterSuccess(ctx, prepareClaimsTotalMetric, 1)
		expectNoDriverErrorResults(ctx)
		beforePrepare, err := rebootCounterValues(ctx, rootFxt, targetNode.Name, prepareClaimsTotalMetric)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		beforePrepareSuccess := beforePrepare["success"]
		beforeUnprepare, err := rebootCounterValues(ctx, rootFxt, targetNode.Name, unprepareClaimsTotalMetric)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		beforeUnprepareSuccess := beforeUnprepare["success"]

		fixture.By("deleting the tester pod while keeping the claim")
		gomega.Expect(e2epod.DeleteSync(ctx, fxt.K8SClientset, createdPod)).To(gomega.Succeed())

		fixture.By("waiting for the driver to release the claim")
		gomega.Eventually(func(g gomega.Gomega) {
			values, err := rebootCounterValues(ctx, rootFxt, targetNode.Name, unprepareClaimsTotalMetric)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(values["success"]).To(gomega.BeNumerically(">=", beforeUnprepareSuccess+1))
			g.Expect(values["error"]).To(gomega.BeZero())
		}, rebootPollTimeout, rebootPollInterval).Should(gomega.Succeed())

		fixture.By("verifying the CDI spec was removed with the claim")
		expectCDISpecForClaim(ctx, injector, claim, false)

		recreatedPod := createTesterPod(ctx, fxt, claim)

		fixture.By("verifying the new container gets the same CPUs")
		expectAssignedCPUs(ctx, fxt, recreatedPod, beforeCPUs)

		fixture.By("verifying the claim was prepared again without errors")
		expectCounterSuccess(ctx, prepareClaimsTotalMetric, beforePrepareSuccess+1)
		expectCounterSuccess(ctx, unprepareClaimsTotalMetric, beforeUnprepareSuccess+1)
		expectNoDriverErrorResults(ctx)
	})

	ginkgo.It("recovers the allocation state after a driver-only restart", func(ctx context.Context) {
		fxt := rootFxt.WithPrefix("driverrestart")
		gomega.Expect(fxt.Setup(ctx)).To(gomega.Succeed())
		ginkgo.DeferCleanup(fxt.Teardown)

		claim := createClaim(ctx, fxt, "claim-driver-restart", 2)
		createdPod := createTesterPod(ctx, fxt, claim)

		fixture.By("recording the allocation before the driver restart")
		beforeCPUs := waitAssignedCPUs(ctx, fxt, createdPod)
		gomega.Expect(beforeCPUs).To(cpusetmatchers.HaveSize(2))
		beforeContainerID := rebootContainerID(createdPod)
		gomega.Expect(beforeContainerID).ToNot(gomega.BeEmpty(), "tester pod has no container yet")
		expectCounterSuccess(ctx, prepareClaimsTotalMetric, 1)
		expectNoDriverErrorResults(ctx)
		fxt.Log.Info("pre-driver-restart state", "cpus", beforeCPUs.String(), "containerID", beforeContainerID)

		ginkgo.DeferCleanup(func(ctx context.Context) {
			restoreDriverDaemonSet(ctx, rootFxt.K8SClientset, orgDaemonSet)
		})

		fixture.By("stopping the driver on node %q", targetNode.Name)
		excludeNodeFromDriverDaemonSet(ctx, rootFxt.K8SClientset, targetNode.Name)
		waitForDriverPodTerminationOnNode(ctx, rootFxt.K8SClientset, targetNode.Name)

		fixture.By("starting the driver again on node %q", targetNode.Name)
		restoreDriverDaemonSet(ctx, rootFxt.K8SClientset, orgDaemonSet)
		waitForDriverPodReadyOnNode(ctx, rootFxt.K8SClientset, targetNode.Name)

		fixture.By("verifying the driver rebuilt its allocation state")
		gomega.Eventually(func(g gomega.Gomega) {
			rawMetrics, err := getDriverRawMetrics(ctx, rootFxt, targetNode.Name)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			snapshot, err := parseAllocationMetricSnapshot(rawMetrics)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(snapshot.AllocatedCPUs).To(gomega.Equal(beforeCPUs.Size()))
			g.Expect(snapshot.ActiveResourceClaims).To(gomega.Equal(1))
		}, rebootPollTimeout, rebootPollInterval).Should(gomega.Succeed())

		fixture.By("verifying the driver rebuilt the state without being asked to prepare again")
		// The NRI plugin's Synchronize hook rebuilds the stores from the running
		// containers' CDI environment, so kubelet has no reason to prepare the
		// claim again and the counters of the new driver process stay at zero.
		gomega.Consistently(func(g gomega.Gomega) {
			prepare, err := rebootCounterValues(ctx, rootFxt, targetNode.Name, prepareClaimsTotalMetric)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(prepare["success"]).To(gomega.BeZero())
			g.Expect(prepare["error"]).To(gomega.BeZero())

			unprepare, err := rebootCounterValues(ctx, rootFxt, targetNode.Name, unprepareClaimsTotalMetric)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(unprepare["success"]).To(gomega.BeZero())
			g.Expect(unprepare["error"]).To(gomega.BeZero())

			current, err := rebootFetchPod(ctx, fxt.K8SClientset, createdPod)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			g.Expect(rebootContainerID(current)).To(gomega.Equal(beforeContainerID), "container was recreated by a driver-only restart")
		}).WithTimeout(rebootConvergeGrace).WithPolling(rebootPollInterval).Should(gomega.Succeed())

		fixture.By("verifying the container kept the same CPUs")
		expectAssignedCPUs(ctx, fxt, createdPod, beforeCPUs)
	})
})

// cdiSpecNameForClaim is the CDI spec file the driver writes for a claim.
func cdiSpecNameForClaim(claim *resourcev1.ResourceClaim) string {
	return fmt.Sprintf("dra.k8s.io-cpu_claim-%s.json", claim.UID)
}

// expectCDISpecForClaim waits until the CDI spec of the given claim is present
// or absent in the CDI spec directory of the node.
func expectCDISpecForClaim(ctx context.Context, injector *e2enode.FaultInjector, claim *resourcev1.ResourceClaim, wantPresent bool) {
	ginkgo.GinkgoHelper()

	specName := cdiSpecNameForClaim(claim)
	gomega.Eventually(func(g gomega.Gomega) {
		names, err := injector.ListCDISpecs(ctx)
		g.Expect(err).ToNot(gomega.HaveOccurred())
		if wantPresent {
			g.Expect(names).To(gomega.ContainElement(specName), "CDI spec %q is missing", specName)
		} else {
			g.Expect(names).ToNot(gomega.ContainElement(specName), "CDI spec %q still exists", specName)
		}
	}, rebootPollTimeout, rebootPollInterval).Should(gomega.Succeed())
}

// expectCDISpecRebuiltAfter waits until the CDI spec of the given claim exists
// and was written after the given time. A reboot wipes the spec directory, so
// the spec the containers need can only be there because the driver wrote it
// again; comparing the modification time proves that, while checking that the
// file merely exists would also pass if nothing had happened.
func expectCDISpecRebuiltAfter(ctx context.Context, injector *e2enode.FaultInjector, claim *resourcev1.ResourceClaim, after time.Time) {
	ginkgo.GinkgoHelper()

	specName := cdiSpecNameForClaim(claim)
	gomega.Eventually(func(g gomega.Gomega) {
		modTime, err := injector.CDISpecModTime(ctx, specName)
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(modTime).To(gomega.BeTemporally(">=", after), "the CDI spec %q was not rewritten after the reboot", specName)
	}, rebootPollTimeout, rebootPollInterval).Should(gomega.Succeed())
}

// expectNodeReady waits until the given node reports the Ready condition again.
func expectNodeReady(ctx context.Context, cs kubernetes.Interface, nodeName string) {
	ginkgo.GinkgoHelper()

	gomega.Eventually(func(g gomega.Gomega) {
		node, err := cs.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		g.Expect(err).ToNot(gomega.HaveOccurred())
		g.Expect(e2enode.IsReady(node)).To(gomega.BeTrue(), "node %q is not Ready", nodeName)
	}, rebootPollTimeout, rebootPollInterval).Should(gomega.Succeed())
}

// rebootFetchPod returns a fresh copy of the given pod.
func rebootFetchPod(ctx context.Context, cs kubernetes.Interface, pod *v1.Pod) (*v1.Pod, error) {
	return cs.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
}

// rebootContainerID returns the ID of the first container of the pod, or an
// empty string when the container is not running yet.
func rebootContainerID(pod *v1.Pod) string {
	if len(pod.Status.ContainerStatuses) == 0 {
		return ""
	}
	return pod.Status.ContainerStatuses[0].ContainerID
}

// rebootPodAssignedCPUs reads the CPUs reported by the tester pod. Unlike
// getTesterPodCPUAllocation it returns errors instead of failing the test, so
// it can be used inside Eventually.
func rebootPodAssignedCPUs(ctx context.Context, cs kubernetes.Interface, pod *v1.Pod) (cpuset.CPUSet, error) {
	data, err := e2epod.GetLogs(ctx, cs, pod)
	if err != nil {
		return cpuset.CPUSet{}, err
	}
	testerInfo := discovery.DRACPUTester{}
	if err := unmarshalLatestReport(data, &testerInfo); err != nil {
		return cpuset.CPUSet{}, err
	}
	return cpuset.Parse(testerInfo.Allocation.CPUs)
}

// rebootCounterValues reads a labeled counter metric from the driver metrics
// endpoint of the node, keyed by label value. A counter which was never
// observed exports no samples at all, which is reported as an empty map.
func rebootCounterValues(ctx context.Context, fxt *fixture.Fixture, nodeName, metricName string) (map[string]float64, error) {
	rawMetrics, err := getDriverRawMetrics(ctx, fxt, nodeName)
	if err != nil {
		return nil, err
	}
	return rebootParseCounterValues(rawMetrics, metricName)
}

func rebootParseCounterValues(rawMetrics, metricName string) (map[string]float64, error) {
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(rawMetrics))
	if err != nil {
		return nil, err
	}

	family, ok := families[metricName]
	if !ok {
		return map[string]float64{}, nil
	}

	values := map[string]float64{}
	for _, metric := range family.Metric {
		if metric.GetCounter() == nil {
			return nil, fmt.Errorf("metric %q is not a counter", metricName)
		}
		result := ""
		for _, label := range metric.Label {
			if label.GetName() == "result" {
				result = label.GetValue()
			}
		}
		values[result] = metric.GetCounter().GetValue()
	}
	return values, nil
}
