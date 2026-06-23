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
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kubernetes-sigs/dra-driver-cpu/api/v1alpha1"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/discovery"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	e2epod "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/pod"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/gcustom"
	"github.com/onsi/gomega/types"
	"github.com/go-logr/logr"
	v1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"
	"k8s.io/utils/cpuset"
	"k8s.io/utils/ptr"
)

func TestE2E(t *testing.T) {
	klog.SetLoggerWithOptions(ginkgo.GinkgoLogr, klog.ContextualLogger(true))
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "DRA CPU Driver E2E Suite")
}

// shared code which is not ready yet to be moved into a test/pkg/... package

const (
	driverName                 = "dra.cpu"
	reasonCreateContainerError = "CreateContainerError"
	argReservedCPUs            = "--reserved-cpus="
	argCPUDeviceMode           = "--cpu-device-mode="
	argGroupBy                 = "--group-by="
	daemonSetNamespace         = "kube-system"
	daemonSetLabel             = "app=dracpu"
	driverPodPollInterval      = 2 * time.Second
	driverPodPollTimeout       = 2 * time.Minute
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

func BeFailedToCreate(logr logr.Logger) types.GomegaMatcher {
	return gcustom.MakeMatcher(func(actual *v1.Pod) (bool, error) {
		if actual == nil {
			return false, errors.New("nil Pod")
		}
		logger := logr.WithValues("podUID", actual.UID, "namespace", actual.Namespace, "name", actual.Name)
		if actual.Status.Phase != v1.PodPending {
			logger.Info("unexpected phase", "phase", actual.Status.Phase)
			return false, nil
		}
		cntSt := findWaitingContainerStatus(actual.Status.ContainerStatuses)
		if cntSt == nil {
			logger.Info("no container in waiting state")
			return false, nil
		}
		if cntSt.State.Waiting.Reason != reasonCreateContainerError {
			logger.Info("container waiting for different reason", "containerName", cntSt.Name, "reason", cntSt.State.Waiting.Reason)
			return false, nil
		}
		logger.Info("container creation error", "containerName", cntSt.Name)
		return true, nil
	}).WithTemplate("Pod {{.Actual.Namespace}}/{{.Actual.Name}} UID {{.Actual.UID}} was not in failed phase")
}

func EventuallyFailedToCreate(ctx context.Context, fxt *fixture.Fixture, pod *v1.Pod) {
	ginkgo.GinkgoHelper()

	gomega.Eventually(func() *v1.Pod {
		pod, err := fxt.K8SClientset.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		if err != nil {
			return nil
		}
		return pod
	}).WithTimeout(time.Minute).WithPolling(2 * time.Second).Should(BeFailedToCreate(fxt.Log))
}

func findWaitingContainerStatus(statuses []v1.ContainerStatus) *v1.ContainerStatus {
	for idx := range statuses {
		cntSt := &statuses[idx]
		if cntSt.State.Waiting != nil {
			return cntSt
		}
	}
	return nil
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

func getTesterPodCPUAllocation(cs kubernetes.Interface, ctx context.Context, pod *v1.Pod) CPUAllocation {
	ginkgo.GinkgoHelper()

	data, err := e2epod.GetLogs(ctx, cs, pod)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get logs for %s/%s/%s", pod.Namespace, pod.Name, pod.Spec.Containers[0].Name)

	// Split logs by newline and find the last non-empty line
	lines := strings.Split(strings.TrimSpace(data), "\n")
	gomega.Expect(len(lines)).To(gomega.BeNumerically(">", 0), "logs should not be empty")
	lastLine := lines[len(lines)-1]

	testerInfo := discovery.DRACPUTester{}
	gomega.Expect(json.Unmarshal([]byte(lastLine), &testerInfo)).To(gomega.Succeed(), "cannot unmarshal last log line: %q", lastLine)

	ret := CPUAllocation{}
	ret.CPUAssigned, err = cpuset.Parse(testerInfo.Allocation.CPUs)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot parse assigned cpuset: %q", testerInfo.Allocation.CPUs)
	ret.CPUAffinity, err = cpuset.Parse(testerInfo.Runtimeinfo.CPUAffinity)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot parse affinity cpuset: %q", testerInfo.Runtimeinfo.CPUAffinity)
	return ret
}

func makeTesterPodWithExclusiveCPUClaim(ns, image, cpuClaimTemplateName string, numCPUs int64, nodeName string) *v1.Pod {
	ginkgo.GinkgoHelper()
	cpuQty := resource.NewQuantity(numCPUs, resource.DecimalSI)
	memQty, err := resource.ParseQuantity("256Mi") // random "low enough" value
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

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
						Requests: v1.ResourceList{
							v1.ResourceCPU:    *cpuQty,
							v1.ResourceMemory: memQty,
						},
						Limits: v1.ResourceList{
							v1.ResourceCPU:    *cpuQty,
							v1.ResourceMemory: memQty,
						},
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
					ResourceClaimTemplateName: ptr.To(cpuClaimTemplateName),
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
		if strings.HasPrefix(arg, prefix) {
			return strings.TrimPrefix(arg, prefix), true
		}
	}
	return "", false
}

func makeTesterPodWithNamedClaim(ns, image, claimName string, nodeName string) *v1.Pod {
	ginkgo.GinkgoHelper()
	cpuQty := resource.NewQuantity(2, resource.DecimalSI)
	memQty, err := resource.ParseQuantity("256Mi")
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

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
						Requests: v1.ResourceList{
							v1.ResourceCPU:    *cpuQty,
							v1.ResourceMemory: memQty,
						},
						Limits: v1.ResourceList{
							v1.ResourceCPU:    *cpuQty,
							v1.ResourceMemory: memQty,
						},
						Claims: []v1.ResourceClaim{
							{Name: "cpu-claim"},
						},
					},
				},
			},
			ResourceClaims: []v1.PodResourceClaim{
				{
					Name:              "cpu-claim",
					ResourceClaimName: ptr.To(claimName),
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
