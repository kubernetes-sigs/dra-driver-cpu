/*
Copyright 2025 The Kubernetes Authors.

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
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kubernetes-sigs/dra-driver-cpu/api/v1alpha1"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/discovery"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	podmatchers "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/matchers/pod"
	e2epod "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/pod"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	v1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/utils/cpuset"
	"sigs.k8s.io/yaml"
)

func TestE2E(t *testing.T) {
	klog.SetLoggerWithOptions(ginkgo.GinkgoLogr, klog.ContextualLogger(true))
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "DRA CPU Driver E2E Suite")
}

// shared code which is not ready yet to be moved into a test/pkg/... package

const (
	driverName            = "dra.cpu"
	argReservedCPUs       = "--reserved-cpus="
	argCPUDeviceMode      = "--cpu-device-mode="
	argGroupBy            = "--group-by="
	daemonSetNamespace    = "kube-system"
	daemonSetLabel        = "app=dracpu"
	driverPodPollInterval = 2 * time.Second
	driverPodPollTimeout  = 2 * time.Minute
)

func listDriverPods(ctx context.Context, client kubernetes.Interface) ([]v1.Pod, error) {
	podList, err := client.CoreV1().Pods(daemonSetNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: daemonSetLabel,
	})
	if err != nil {
		return nil, fmt.Errorf("listing pods with selector %q in %q: %w",
			daemonSetLabel, daemonSetNamespace, err)
	}
	return podList.Items, nil
}

func waitForRunningDriverPods(ctx context.Context, client kubernetes.Interface) []v1.Pod {
	ginkgo.GinkgoHelper()

	var pods []v1.Pod
	gomega.Eventually(func(g gomega.Gomega) {
		var err error
		pods, err = listDriverPods(ctx, client)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(pods).NotTo(gomega.BeEmpty(),
			"no dra-driver-cpu pods found with selector %q in namespace %q",
			daemonSetLabel, daemonSetNamespace)
		for _, pod := range pods {
			g.Expect(pod.Status.Phase).To(gomega.Equal(v1.PodRunning),
				"pod %q on node %q is not Running (phase=%s)",
				pod.Name, pod.Spec.NodeName, pod.Status.Phase)
		}
	}, driverPodPollTimeout, driverPodPollInterval).Should(gomega.Succeed(),
		"timed out waiting for dra-driver-cpu pods to reach Running phase")

	return pods
}

func EventuallyFailedToCreate(ctx context.Context, fxt *fixture.Fixture, pod *v1.Pod) {
	ginkgo.GinkgoHelper()

	gomega.Eventually(func() *v1.Pod {
		pod, err := fxt.K8SClientset.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return nil
		}
		return pod
	}).WithTimeout(time.Minute).WithPolling(2 * time.Second).Should(podmatchers.BeFailedToCreate(fxt.Log))
}

// expectNodeAllocatableClaimStatus verifies the scheduler-populated node allocatable claim status
// on a pod using one CPU claim: with the node allocatable mapping enabled it must report the
// claim's CPUs, otherwise the field must be absent. The scheduler writes the status at
// PreBind, so it is final by the time the pod runs; polling is defensive.
func expectNodeAllocatableClaimStatus(ctx context.Context, cs kubernetes.Interface, pod *v1.Pod, wantCPUs int64, nodeAllocatableMapping bool) {
	ginkgo.GinkgoHelper()

	if !nodeAllocatableMapping {
		current, err := cs.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		gomega.Expect(current.Status.NodeAllocatableResourceClaimStatuses).To(gomega.BeEmpty(),
			"nodeAllocatableResourceClaimStatuses must be absent when the node allocatable mapping is disabled")
		return
	}

	// The claim status must cover exactly the containers that reference a claim.
	var claimContainers []string
	for _, cnt := range pod.Spec.Containers {
		if len(cnt.Resources.Claims) > 0 {
			claimContainers = append(claimContainers, cnt.Name)
		}
	}
	gomega.Expect(claimContainers).ToNot(gomega.BeEmpty(),
		"pod %s/%s has no containers referencing resource claims", pod.Namespace, pod.Name)

	gomega.Eventually(func(ctx context.Context) (*v1.Pod, error) {
		return cs.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
	}).WithContext(ctx).WithTimeout(time.Minute).WithPolling(2 * time.Second).
		Should(podmatchers.HaveNodeAllocatableClaimStatus(claimContainers, wantCPUs))
}

// getDriverConfig fetches the dracpu DaemonSet and returns the deployed driver's
// configuration via getDriverConfigValues. Fields the deployment does not set are
// zero values, mirroring the driver's own defaults for booleans.
func getDriverConfig(ctx context.Context, cs kubernetes.Interface) driverConfigValues {
	ginkgo.GinkgoHelper()
	daemonSet, err := cs.AppsV1().DaemonSets(daemonSetNamespace).Get(ctx, "dracpu", metav1.GetOptions{})
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get dracpu daemonset")
	values, err := getDriverConfigValues(ctx, cs, daemonSetNamespace, daemonSet)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot read dracpu driver config values")
	return values
}

func makeCPUSetFromDiscoveredCPUInfo(cpuInfo discovery.DRACPUInfo) cpuset.CPUSet {
	coreIDs := make([]int, len(cpuInfo.CPUs))
	for idx, cpu := range cpuInfo.CPUs {
		coreIDs[idx] = cpu.CpuID
	}
	return cpuset.New(coreIDs...)
}

type CPUAllocation struct {
	CPUAssigned cpuset.CPUSet
	CPUAffinity cpuset.CPUSet
}

func unmarshalLatestReport(data string, v any) error {
	lines := strings.Split(strings.TrimSpace(data), "\n")
	var lastErr error
	for _, line := range slices.Backward(lines) {
		err := json.Unmarshal([]byte(line), v)
		if err == nil {
			return nil
		}
		lastErr = err
	}
	return fmt.Errorf("no JSON line found in %d log lines: %w", len(lines), lastErr)
}

func getTesterPodCPUAllocation(cs kubernetes.Interface, ctx context.Context, pod *v1.Pod) CPUAllocation {
	ginkgo.GinkgoHelper()

	data, err := e2epod.GetLogs(ctx, cs, pod)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get logs for %s/%s/%s", pod.Namespace, pod.Name, pod.Spec.Containers[0].Name)

	testerInfo := discovery.DRACPUTester{}
	gomega.Expect(unmarshalLatestReport(data, &testerInfo)).To(gomega.Succeed(), "cannot unmarshal tester report from logs")

	ret := CPUAllocation{}
	ret.CPUAssigned, err = cpuset.Parse(testerInfo.Allocation.CPUs)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot parse assigned cpuset: %q", testerInfo.Allocation.CPUs)
	ret.CPUAffinity, err = cpuset.Parse(testerInfo.Runtimeinfo.CPUAffinity)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot parse affinity cpuset: %q", testerInfo.Runtimeinfo.CPUAffinity)
	return ret
}

// claimContainerResources returns the standard resources for a container using a CPU claim
// of numCPUs, following docs/user/workload-requirements.md for the given mode:
// with the node allocatable mapping the claim's CPUs must not be duplicated in the spec,
// without it they must be mirrored in requests and limits.
func claimContainerResources(numCPUs int64, nodeAllocatableMapping bool) (requests, limits v1.ResourceList) {
	ginkgo.GinkgoHelper()
	memQty, err := resource.ParseQuantity("256Mi") // random "low enough" value
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	requests = v1.ResourceList{v1.ResourceMemory: memQty}
	limits = v1.ResourceList{v1.ResourceMemory: memQty}
	if !nodeAllocatableMapping {
		cpuQty := resource.NewQuantity(numCPUs, resource.DecimalSI)
		requests[v1.ResourceCPU] = *cpuQty
		limits[v1.ResourceCPU] = *cpuQty
	}
	return requests, limits
}

func makeTesterPodWithExclusiveCPUClaim(ns, image, cpuClaimTemplateName string, numCPUs int64, nodeName string, nodeAllocatableMapping bool) *v1.Pod {
	ginkgo.GinkgoHelper()
	requests, limits := claimContainerResources(numCPUs, nodeAllocatableMapping)

	podWithClaim := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "tester-pod-excl-cpu-claim-",
			Namespace:    ns,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:    "tester-container-1",
					Image:   image,
					Command: []string{"/dracputester"},
					Resources: v1.ResourceRequirements{
						Requests: requests,
						Limits:   limits,
						Claims: []v1.ResourceClaim{
							{
								Name: "tester-container-1-claim",
							},
						},
					},
				},
			},
			ResourceClaims: []v1.PodResourceClaim{
				{
					Name:                      "tester-container-1-claim",
					ResourceClaimTemplateName: new(cpuClaimTemplateName),
				},
			},
			RestartPolicy: v1.RestartPolicyAlways,
		},
	}
	return e2epod.PinToNode(podWithClaim, nodeName)
}

func makeTesterPodBestEffort(ns, image string) *v1.Pod {
	return &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "tester-pod-be-",
			Namespace:    ns,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:    "tester-container",
					Image:   image,
					Command: []string{"/dracputester"},
					// at the moment we depend on pod logs to learn the cpu allocation.
					// Therefore, the pod without resource claims, best-effort,
					// will loop periodically to provide the up to date information.
					// NOTE: We parse the last line of the logs to get the latest update.
				},
			},
			RestartPolicy: v1.RestartPolicyAlways,
		},
	}
}

func mustCreateBestEffortPod(ctx context.Context, fxt *fixture.Fixture, nodeName, dracpuTesterImage string) *v1.Pod {
	fixture.By("creating a best-effort reference pod")
	pod := makeTesterPodBestEffort(fxt.Namespace.Name, dracpuTesterImage)
	pod = e2epod.PinToNode(pod, nodeName)
	pod, err := e2epod.CreateSync(ctx, fxt.K8SClientset, pod)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create tester pod: %v", err)
	return pod
}

func findArgInContainer(container *v1.Container, prefix string) (string, bool) {
	for _, arg := range container.Args {
		if after, ok := strings.CutPrefix(arg, prefix); ok {
			return after, true
		}
	}
	return "", false
}

// driverConfigValues holds cpuDeviceMode/groupBy/reservedCPUs read back from a daemonset.
type driverConfigValues struct {
	CPUDeviceMode                         string `json:"cpuDeviceMode,omitempty"`
	GroupBy                               string `json:"groupBy,omitempty"`
	ReservedCPUs                          string `json:"reservedCPUs,omitempty"`
	PublishNodeAllocatableResourceMapping bool   `json:"publishNodeAllocatableResourceMapping,omitempty"`
}

// getDriverConfigValues reads the ConfigMap if the daemonset uses --config=, else falls back to
// the deprecated CLI flags.
func getDriverConfigValues(ctx context.Context, client kubernetes.Interface, namespace string, ds *appsv1.DaemonSet) (driverConfigValues, error) {
	var values driverConfigValues
	cnt := &ds.Spec.Template.Spec.Containers[0]
	if _, ok := findArgInContainer(cnt, "--config="); ok {
		cm, err := client.CoreV1().ConfigMaps(namespace).Get(ctx, ds.Name+"-config", metav1.GetOptions{})
		if err != nil {
			return values, fmt.Errorf("getting driver config configmap for daemonset %s: %w", ds.Name, err)
		}
		if err := yaml.Unmarshal([]byte(cm.Data["config.yaml"]), &values); err != nil {
			return values, fmt.Errorf("parsing driver config configmap for daemonset %s: %w", ds.Name, err)
		}
		return values, nil
	}
	if val, ok := findArgInContainer(cnt, argCPUDeviceMode); ok {
		values.CPUDeviceMode = val
	}
	if val, ok := findArgInContainer(cnt, argGroupBy); ok {
		values.GroupBy = val
	}
	if val, ok := findArgInContainer(cnt, argReservedCPUs); ok {
		values.ReservedCPUs = val
	}
	return values, nil
}

func makeTesterPodWithNamedClaim(ns, image, claimName string, nodeName string, nodeAllocatableMapping bool) *v1.Pod {
	ginkgo.GinkgoHelper()
	requests, limits := claimContainerResources(2, nodeAllocatableMapping)

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: "tester-pod-named-claim-",
			Namespace:    ns,
		},
		Spec: v1.PodSpec{
			Containers: []v1.Container{
				{
					Name:    "tester-container-1",
					Image:   image,
					Command: []string{"/dracputester"},
					Resources: v1.ResourceRequirements{
						Requests: requests,
						Limits:   limits,
						Claims: []v1.ResourceClaim{
							{Name: "cpu-claim"},
						},
					},
				},
			},
			ResourceClaims: []v1.PodResourceClaim{
				{
					Name:              "cpu-claim",
					ResourceClaimName: new(claimName),
				},
			},
			RestartPolicy: v1.RestartPolicyAlways,
		},
	}
	return e2epod.PinToNode(pod, nodeName)
}

func makeResourceClaimSpec(cpus int, isConsumable bool) resourcev1.ResourceClaimSpec {
	if !isConsumable {
		return resourcev1.ResourceClaimSpec{
			Devices: resourcev1.DeviceClaim{
				Requests: []resourcev1.DeviceRequest{
					{
						Name: "request-cpus",
						Exactly: &resourcev1.ExactDeviceRequest{
							DeviceClassName: driverName,
							Count:           int64(cpus),
						},
					},
				},
			},
		}
	}
	return resourcev1.ResourceClaimSpec{
		Devices: resourcev1.DeviceClaim{
			Requests: []resourcev1.DeviceRequest{
				{
					Name: "request-cpus",
					Exactly: &resourcev1.ExactDeviceRequest{
						DeviceClassName: driverName,
						Capacity: &resourcev1.CapacityRequirements{
							Requests: map[resourcev1.QualifiedName]resource.Quantity{
								"dra.cpu/cpu": *resource.NewQuantity(int64(cpus), resource.DecimalSI),
							},
						},
					},
				},
			},
		},
	}
}

func makeResourceClaimSpecWithOpaqueConfig(cpus int, isConsumable bool, cpusetStr string) resourcev1.ResourceClaimSpec {
	ginkgo.GinkgoHelper()
	spec := makeResourceClaimSpec(cpus, isConsumable)
	if cpusetStr != "" {
		config := v1alpha1.OpaqueConfig{
			APIVersion: v1alpha1.APIVersion,
			CPUConfig: v1alpha1.CPUConfig{
				CPUSet: cpusetStr,
			},
		}
		rawConfig, err := json.Marshal(config)
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		spec.Devices.Config = []resourcev1.DeviceClaimConfiguration{
			{
				Requests: []string{spec.Devices.Requests[0].Name},
				DeviceConfiguration: resourcev1.DeviceConfiguration{
					Opaque: &resourcev1.OpaqueDeviceConfiguration{
						Driver: driverName,
						Parameters: runtime.RawExtension{
							Raw: rawConfig,
						},
					},
				},
			},
		}
	}
	return spec
}
