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
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/discovery"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	e2enode "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/node"
	e2epod "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/pod"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	dto "github.com/prometheus/client_model/go"
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
	driverDaemonSetName = "dracpu"
	metricsPath         = "/metrics"

	allocatedCPUsMetric        = "dra_cpu_allocated_cpus"
	availableCPUsMetric        = "dra_cpu_available_cpus"
	reservedCPUsMetric         = "dra_cpu_reserved_cpus"
	activeResourceClaimsMetric = "dra_cpu_resource_claims_active"
)

type allocationMetricSnapshot struct {
	AllocatedCPUs        int
	AvailableCPUs        int
	ReservedCPUs         int
	ActiveResourceClaims int
}

func (s allocationMetricSnapshot) TotalCPUs() int {
	return s.AllocatedCPUs + s.AvailableCPUs + s.ReservedCPUs
}

func (s allocationMetricSnapshot) String() string {
	return fmt.Sprintf("allocated=%d available=%d reserved=%d activeClaims=%d total=%d",
		s.AllocatedCPUs, s.AvailableCPUs, s.ReservedCPUs, s.ActiveResourceClaims, s.TotalCPUs())
}

// This serial spec complements the unit coverage in pkg/driver/metrics_test.go
// with a real-cluster check that gauge state survives NRI restart and is released
// again when the workload stops.
var _ = ginkgo.Describe("Metrics", ginkgo.Serial, func() {
	ginkgo.It("reconciles allocation metrics across driver restart and workload deletion", func(ctx context.Context) {
		dracpuTesterImage := os.Getenv("DRACPU_E2E_TEST_IMAGE")
		gomega.Expect(dracpuTesterImage).ToNot(gomega.BeEmpty(), "missing environment variable DRACPU_E2E_TEST_IMAGE")

		rootFxt, err := fixture.ForGinkgo()
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create root fixture: %v", err)
		metricsFxt := rootFxt.WithPrefix("metrics")
		gomega.Expect(metricsFxt.Setup(ctx)).To(gomega.Succeed())
		ginkgo.DeferCleanup(metricsFxt.Teardown)

		targetNode, err := e2enode.PickWorker(ctx, metricsFxt.K8SClientset, 5*time.Second, 1*time.Minute, metricsFxt.Log)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		metricsFxt.Log.Info("using worker node", "nodeName", targetNode.Name)

		daemonSet, err := rootFxt.K8SClientset.AppsV1().DaemonSets(daemonSetNamespace).Get(ctx, driverDaemonSetName, metav1.GetOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get dracpu daemonset")
		gomega.Expect(daemonSet.Spec.Template.Spec.Containers).ToNot(gomega.BeEmpty(), "no containers in dracpu daemonset")
		orgDaemonSet := daemonSet.DeepCopy()

		cfgValues, err := getDriverConfigValues(ctx, rootFxt.K8SClientset, daemonSetNamespace, daemonSet)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot read dracpu driver config values")
		cpuDeviceMode := cfgValues.CPUDeviceMode
		groupBy := cfgValues.GroupBy

		var claimSpec resourcev1.ResourceClaimSpec
		if cpuDeviceMode == "grouped" && groupBy == "machine" {
			var reservedCPUs cpuset.CPUSet
			if len(cfgValues.ReservedCPUs) > 0 {
				reservedCPUs, err = cpuset.Parse(cfgValues.ReservedCPUs)
				gomega.Expect(err).ToNot(gomega.HaveOccurred())
			}
			availableCPUs, err := discoverAvailableCPUs(ctx, metricsFxt, targetNode.Name, dracpuTesterImage, reservedCPUs)
			gomega.Expect(err).ToNot(gomega.HaveOccurred())
			if availableCPUs.Size() == 0 {
				ginkgo.Skip(fmt.Sprintf("no allocatable CPUs left on node %q", targetNode.Name))
			}

			cpuID := availableCPUs.UnsortedList()[0]
			claimSpec = makeResourceClaimSpecWithOpaqueConfig(1, true, cpuset.New(cpuID).String())
		} else {
			claimSpec = makeResourceClaimSpec(1, cpuDeviceMode == "grouped")
		}

		baseline, err := getDriverAllocationMetrics(ctx, metricsFxt.K8SClientset, targetNode.Name)
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get baseline allocation metrics")
		expectBasicMetricPropertiesNow(baseline)
		if baseline.AvailableCPUs < 1 {
			ginkgo.Skip(fmt.Sprintf("no available CPUs reported on node %q: %s", targetNode.Name, baseline.String()))
		}

		claimTemplate := resourcev1.ResourceClaimTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name: "cpu-claim-metrics",
			},
			Spec: resourcev1.ResourceClaimTemplateSpec{
				Spec: claimSpec,
			},
		}
		createdClaimTemplate, err := metricsFxt.K8SClientset.ResourceV1().ResourceClaimTemplates(metricsFxt.Namespace.Name).Create(ctx, &claimTemplate, metav1.CreateOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		testerPod := makeTesterPodWithExclusiveCPUClaim(metricsFxt.Namespace.Name, dracpuTesterImage, createdClaimTemplate.Name, 1, targetNode.Name, getDriverConfig(ctx, metricsFxt.K8SClientset).PublishNodeAllocatableResourceMapping)
		createdPod, err := e2epod.CreateSync(ctx, metricsFxt.K8SClientset, testerPod)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		ginkgo.By("verifying allocation metrics reflect the new claim")
		gomega.Eventually(func(g gomega.Gomega) {
			snapshot, err := getDriverAllocationMetrics(ctx, metricsFxt.K8SClientset, targetNode.Name)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			expectBasicMetricProperties(g, snapshot)
			g.Expect(snapshot.TotalCPUs()).To(gomega.Equal(baseline.TotalCPUs()))
			g.Expect(snapshot.AllocatedCPUs).To(gomega.Equal(baseline.AllocatedCPUs + 1))
			g.Expect(snapshot.AvailableCPUs).To(gomega.Equal(baseline.AvailableCPUs - 1))
			g.Expect(snapshot.ReservedCPUs).To(gomega.Equal(baseline.ReservedCPUs))
			g.Expect(snapshot.ActiveResourceClaims).To(gomega.Equal(baseline.ActiveResourceClaims + 1))
		}).WithTimeout(driverPodPollTimeout).WithPolling(driverPodPollInterval).Should(gomega.Succeed())

		ginkgo.DeferCleanup(func(ctx context.Context) {
			restoreDriverDaemonSet(ctx, rootFxt.K8SClientset, orgDaemonSet)
		})

		ginkgo.By("stopping the driver on the target node")
		excludeNodeFromDriverDaemonSet(ctx, rootFxt.K8SClientset, targetNode.Name)
		waitForDriverPodTerminationOnNode(ctx, rootFxt.K8SClientset, targetNode.Name)

		ginkgo.By("restoring the driver on the target node")
		restoreDriverDaemonSet(ctx, rootFxt.K8SClientset, orgDaemonSet)
		waitForDriverPodReadyOnNode(ctx, rootFxt.K8SClientset, targetNode.Name)

		ginkgo.By("verifying allocation metrics were rebuilt after restart")
		gomega.Eventually(func(g gomega.Gomega) {
			snapshot, err := getDriverAllocationMetrics(ctx, metricsFxt.K8SClientset, targetNode.Name)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			expectBasicMetricProperties(g, snapshot)
			g.Expect(snapshot.TotalCPUs()).To(gomega.Equal(baseline.TotalCPUs()))
			g.Expect(snapshot.AllocatedCPUs).To(gomega.Equal(baseline.AllocatedCPUs + 1))
			g.Expect(snapshot.AvailableCPUs).To(gomega.Equal(baseline.AvailableCPUs - 1))
			g.Expect(snapshot.ReservedCPUs).To(gomega.Equal(baseline.ReservedCPUs))
			g.Expect(snapshot.ActiveResourceClaims).To(gomega.Equal(baseline.ActiveResourceClaims + 1))
		}).WithTimeout(driverPodPollTimeout).WithPolling(driverPodPollInterval).Should(gomega.Succeed())

		ginkgo.By("deleting the workload and verifying allocation metrics return to baseline")
		gomega.Expect(e2epod.DeleteSync(ctx, metricsFxt.K8SClientset, createdPod)).To(gomega.Succeed(), "cannot delete tester pod %s", e2epod.Identify(createdPod))
		gomega.Eventually(func(g gomega.Gomega) {
			snapshot, err := getDriverAllocationMetrics(ctx, metricsFxt.K8SClientset, targetNode.Name)
			g.Expect(err).ToNot(gomega.HaveOccurred())
			expectBasicMetricProperties(g, snapshot)
			g.Expect(snapshot).To(gomega.Equal(baseline))
		}).WithTimeout(driverPodPollTimeout).WithPolling(driverPodPollInterval).Should(gomega.Succeed())
	})
})

func discoverAvailableCPUs(ctx context.Context, fxt *fixture.Fixture, nodeName, image string, reservedCPUs cpuset.CPUSet) (cpuset.CPUSet, error) {
	infoPod := discovery.MakePod(fxt.Namespace.Name, image)
	infoPod = e2epod.PinToNode(infoPod, nodeName)
	infoPod, err := e2epod.RunToCompletion(ctx, fxt.K8SClientset, infoPod)
	if err != nil {
		return cpuset.CPUSet{}, err
	}

	data, err := e2epod.GetLogs(ctx, fxt.K8SClientset, infoPod)
	if err != nil {
		return cpuset.CPUSet{}, err
	}

	var targetNodeCPUInfo discovery.DRACPUInfo
	if err := json.Unmarshal([]byte(data), &targetNodeCPUInfo); err != nil {
		return cpuset.CPUSet{}, err
	}

	allocatableCPUs := makeCPUSetFromDiscoveredCPUInfo(targetNodeCPUInfo)
	return allocatableCPUs.Difference(reservedCPUs), nil
}

func getDriverAllocationMetrics(ctx context.Context, cs kubernetes.Interface, nodeName string) (allocationMetricSnapshot, error) {
	dracpuPod, err := e2epod.GetDRACPUPod(ctx, cs, nodeName)
	if err != nil {
		return allocationMetricSnapshot{}, err
	}

	podIP, err := waitForPodIP(ctx, cs, dracpuPod.Name)
	if err != nil {
		return allocationMetricSnapshot{}, err
	}

	rawMetrics, err := getMetricsFromPodIP(podIP)
	if err != nil {
		return allocationMetricSnapshot{}, fmt.Errorf("cannot get metrics from the dracpu pod: %w", err)
	}
	return parseAllocationMetricSnapshot(rawMetrics)
}

func metricsURL(podIP string) string {
	return fmt.Sprintf("http://%s:%d%s", podIP, driverHTTPPort, metricsPath)
}

func getMetricsFromPodIP(podIP string) (string, error) {
	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Get(metricsURL(podIP)) //nolint:noctx // httpClient.Timeout covers this
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GET %s returned %d: %s", metricsURL(podIP), resp.StatusCode, string(bodyBytes))
	}
	return string(bodyBytes), nil
}

func parseAllocationMetricSnapshot(rawMetrics string) (allocationMetricSnapshot, error) {
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(strings.NewReader(rawMetrics))
	if err != nil {
		return allocationMetricSnapshot{}, err
	}

	allocated, err := readSingleGaugeMetric(families, allocatedCPUsMetric)
	if err != nil {
		return allocationMetricSnapshot{}, err
	}
	available, err := readSingleGaugeMetric(families, availableCPUsMetric)
	if err != nil {
		return allocationMetricSnapshot{}, err
	}
	reserved, err := readSingleGaugeMetric(families, reservedCPUsMetric)
	if err != nil {
		return allocationMetricSnapshot{}, err
	}
	activeClaims, err := readSingleGaugeMetric(families, activeResourceClaimsMetric)
	if err != nil {
		return allocationMetricSnapshot{}, err
	}

	return allocationMetricSnapshot{
		AllocatedCPUs:        allocated,
		AvailableCPUs:        available,
		ReservedCPUs:         reserved,
		ActiveResourceClaims: activeClaims,
	}, nil
}

func readSingleGaugeMetric(families map[string]*dto.MetricFamily, metricName string) (int, error) {
	family, ok := families[metricName]
	if !ok {
		return 0, fmt.Errorf("metric %q not found", metricName)
	}
	if family.GetType() != dto.MetricType_GAUGE {
		return 0, fmt.Errorf("metric %q has unexpected type %q", metricName, family.GetType())
	}
	if len(family.Metric) != 1 {
		return 0, fmt.Errorf("metric %q expected one sample, got %d", metricName, len(family.Metric))
	}
	if len(family.Metric[0].Label) != 0 {
		return 0, fmt.Errorf("metric %q unexpectedly has labels", metricName)
	}
	value := family.Metric[0].GetGauge().GetValue()
	if value < 0 {
		return 0, fmt.Errorf("metric %q must be non-negative, got %v", metricName, value)
	}
	if value != math.Trunc(value) {
		return 0, fmt.Errorf("metric %q must be integral, got %v", metricName, value)
	}
	return int(value), nil
}

func expectBasicMetricProperties(g gomega.Gomega, snapshot allocationMetricSnapshot) {
	g.Expect(snapshot.AllocatedCPUs).To(gomega.BeNumerically(">=", 0), "allocated CPUs must be non-negative")
	g.Expect(snapshot.AvailableCPUs).To(gomega.BeNumerically(">=", 0), "available CPUs must be non-negative")
	g.Expect(snapshot.ReservedCPUs).To(gomega.BeNumerically(">=", 0), "reserved CPUs must be non-negative")
	g.Expect(snapshot.ActiveResourceClaims).To(gomega.BeNumerically(">=", 0), "active resource claims must be non-negative")
	g.Expect(snapshot.TotalCPUs()).To(gomega.BeNumerically(">", 0), "total CPUs derived from metrics must be positive")
}

func expectBasicMetricPropertiesNow(snapshot allocationMetricSnapshot) {
	ginkgo.GinkgoHelper()

	gomega.Expect(snapshot.AllocatedCPUs).To(gomega.BeNumerically(">=", 0), "allocated CPUs must be non-negative")
	gomega.Expect(snapshot.AvailableCPUs).To(gomega.BeNumerically(">=", 0), "available CPUs must be non-negative")
	gomega.Expect(snapshot.ReservedCPUs).To(gomega.BeNumerically(">=", 0), "reserved CPUs must be non-negative")
	gomega.Expect(snapshot.ActiveResourceClaims).To(gomega.BeNumerically(">=", 0), "active resource claims must be non-negative")
	gomega.Expect(snapshot.TotalCPUs()).To(gomega.BeNumerically(">", 0), "total CPUs derived from metrics must be positive")
}

func excludeNodeFromDriverDaemonSet(ctx context.Context, cs kubernetes.Interface, nodeName string) {
	ginkgo.GinkgoHelper()

	gomega.Eventually(func(g gomega.Gomega) {
		ds, err := cs.AppsV1().DaemonSets(daemonSetNamespace).Get(ctx, driverDaemonSetName, metav1.GetOptions{})
		g.Expect(err).ToNot(gomega.HaveOccurred())

		req := v1.NodeSelectorRequirement{
			Key:      "kubernetes.io/hostname",
			Operator: v1.NodeSelectorOpNotIn,
			Values:   []string{nodeName},
		}
		if ds.Spec.Template.Spec.Affinity == nil {
			ds.Spec.Template.Spec.Affinity = &v1.Affinity{}
		}
		if ds.Spec.Template.Spec.Affinity.NodeAffinity == nil {
			ds.Spec.Template.Spec.Affinity.NodeAffinity = &v1.NodeAffinity{}
		}
		if ds.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
			ds.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &v1.NodeSelector{}
		}
		terms := ds.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
		if len(terms) == 0 {
			ds.Spec.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = []v1.NodeSelectorTerm{{
				MatchExpressions: []v1.NodeSelectorRequirement{req},
			}}
		} else {
			for i := range terms {
				terms[i].MatchExpressions = append(terms[i].MatchExpressions, req)
			}
		}

		_, err = cs.AppsV1().DaemonSets(daemonSetNamespace).Update(ctx, ds, metav1.UpdateOptions{})
		g.Expect(err).ToNot(gomega.HaveOccurred())
	}).WithTimeout(driverPodPollTimeout).WithPolling(driverPodPollInterval).Should(gomega.Succeed(), "failed to update DaemonSet affinity")
}

func restoreDriverDaemonSet(ctx context.Context, cs kubernetes.Interface, orgDaemonSet *appsv1.DaemonSet) {
	ginkgo.GinkgoHelper()

	gomega.Eventually(func(g gomega.Gomega) {
		ds, err := cs.AppsV1().DaemonSets(daemonSetNamespace).Get(ctx, driverDaemonSetName, metav1.GetOptions{})
		g.Expect(err).ToNot(gomega.HaveOccurred())

		ds.Spec = orgDaemonSet.Spec
		_, err = cs.AppsV1().DaemonSets(daemonSetNamespace).Update(ctx, ds, metav1.UpdateOptions{})
		g.Expect(err).ToNot(gomega.HaveOccurred())
	}).WithTimeout(driverPodPollTimeout).WithPolling(driverPodPollInterval).Should(gomega.Succeed())
}

func waitForDriverPodTerminationOnNode(ctx context.Context, cs kubernetes.Interface, nodeName string) {
	ginkgo.GinkgoHelper()

	gomega.Eventually(func(g gomega.Gomega) {
		pods, err := listDriverPods(ctx, cs)
		g.Expect(err).ToNot(gomega.HaveOccurred())

		terminated := true
		for _, pod := range pods {
			if pod.Spec.NodeName == nodeName && pod.Status.Phase != v1.PodFailed && pod.Status.Phase != v1.PodSucceeded {
				terminated = false
				break
			}
		}
		g.Expect(terminated).To(gomega.BeTrue(), "driver pod on node %q is still running", nodeName)
	}).WithTimeout(driverPodPollTimeout).WithPolling(driverPodPollInterval).Should(gomega.Succeed(), "timed out waiting for pod to terminate")
}

func waitForDriverPodReadyOnNode(ctx context.Context, cs kubernetes.Interface, nodeName string) {
	ginkgo.GinkgoHelper()

	gomega.Eventually(func(g gomega.Gomega) {
		pods, err := listDriverPods(ctx, cs)
		g.Expect(err).ToNot(gomega.HaveOccurred())

		ready := false
		for _, pod := range pods {
			if pod.Spec.NodeName != nodeName {
				continue
			}
			for _, containerStatus := range pod.Status.ContainerStatuses {
				if containerStatus.Ready {
					ready = true
					break
				}
			}
			if ready {
				break
			}
		}
		g.Expect(ready).To(gomega.BeTrue(), "driver pod on node %q is not ready", nodeName)
	}).WithTimeout(driverPodPollTimeout).WithPolling(driverPodPollInterval).Should(gomega.Succeed(), "timed out waiting for pod to become ready")
}
