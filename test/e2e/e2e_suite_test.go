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
	"strings"
	"testing"

	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/discovery"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	e2epod "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/pod"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/gcustom"
	"github.com/onsi/gomega/types"
	v1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/utils/cpuset"
	"k8s.io/utils/ptr"
)

func TestE2E(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "DRA CPU Driver E2E Suite")
}

// shared code which is not ready yet to be moved into a test/pkg/... package

const (
	reasonCreateContainerError = "CreateContainerError"
)

func BeFailedToCreate(fxt *fixture.Fixture) types.GomegaMatcher {
	return gcustom.MakeMatcher(func(actual *v1.Pod) (bool, error) {
		if actual == nil {
			return false, errors.New("nil Pod")
		}
		lh := fxt.Log.WithValues("podUID", actual.UID, "namespace", actual.Namespace, "name", actual.Name)
		if actual.Status.Phase != v1.PodPending {
			lh.Info("unexpected phase", "phase", actual.Status.Phase)
			return false, nil
		}
		cntSt := findWaitingContainerStatus(actual.Status.ContainerStatuses)
		if cntSt == nil {
			lh.Info("no container in waiting state")
			return false, nil
		}
		if cntSt.State.Waiting.Reason != reasonCreateContainerError {
			lh.Info("container terminated for different reason", "containerName", cntSt.Name, "reason", cntSt.State.Terminated.Reason)
			return false, nil
		}
		lh.Info("container creation error", "containerName", cntSt.Name)
		return true, nil
	}).WithTemplate("Pod {{.Actual.Namespace}}/{{.Actual.Name}} UID {{.Actual.UID}} was not in failed phase")
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

	data, err := e2epod.GetLogs(cs, ctx, pod.Namespace, pod.Name, pod.Spec.Containers[0].Name)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get logs for %s/%s/%s", pod.Namespace, pod.Name, pod.Spec.Containers[0].Name)

	testerInfo := discovery.DRACPUTester{}
	gomega.Expect(json.Unmarshal([]byte(data), &testerInfo)).To(gomega.Succeed())

	ret := CPUAllocation{}
	ret.CPUAssigned, err = cpuset.Parse(testerInfo.Allocation.CPUs)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot parse assigned cpuset: %q", testerInfo.Allocation.CPUs)
	ret.CPUAffinity, err = cpuset.Parse(testerInfo.Runtimeinfo.CPUAffinity)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot parse affinity cpuset: %q", testerInfo.Runtimeinfo.CPUAffinity)
	return ret
}

func makeTesterPodWithExclusiveCPUClaim(ns, image, cpuClaimTemplateName string, numCPUs int64) *v1.Pod {
	ginkgo.GinkgoHelper()
	cpuQty := resource.NewQuantity(numCPUs, resource.DecimalSI)
	memQty, err := resource.ParseQuantity("256Mi") // random "low enough" value
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	return &v1.Pod{
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
					// will restart periodically to provide the up to date information.
					// NOTE: restarting always ensuring there's the minimal and easiest
					// amount of logs to process. The other option would be to periodically
					// log the data, but then the client side will need to read and discard
					// all but the latest update, which is wasteful. This approach is the simplest.
					Args: []string{"-run-for", "10s"},
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

func parseReservedCPUsArg(arg string) (string, bool) {
	prefix := "--reserved-cpus="
	if !strings.HasPrefix(arg, prefix) {
		return "", false
	}
	return strings.TrimPrefix(arg, prefix), true
}

func parseCPUDeviceModeArg(arg string) (string, bool) {
	prefix := "--cpu-device-mode="
	if !strings.HasPrefix(arg, prefix) {
		return "", false
	}
	return strings.TrimPrefix(arg, prefix), true
}

func makeResourceClaimSpec(cpus int, isConsumable bool) resourcev1.ResourceClaimSpec {
	if !isConsumable {
		return resourcev1.ResourceClaimSpec{
			Devices: resourcev1.DeviceClaim{
				Requests: []resourcev1.DeviceRequest{
					{
						Name: "request-cpus",
						Exactly: &resourcev1.ExactDeviceRequest{
							DeviceClassName: "dra.cpu",
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
						DeviceClassName: "dra.cpu",
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
