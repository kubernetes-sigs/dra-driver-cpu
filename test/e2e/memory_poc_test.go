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
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/discovery"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	e2enode "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/node"
	e2epod "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/pod"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/cpuset"
)

var _ = ginkgo.Describe("Memory integration POC", ginkgo.Serial, ginkgo.Label("memory-poc"), func() {
	var fxt *fixture.Fixture
	var node *corev1.Node
	var candidates map[int64]map[string]bool
	var image string
	ginkgo.BeforeEach(func(ctx context.Context) {
		if os.Getenv("DRACPU_E2E_MEMORY_POC") != "true" {
			ginkgo.Skip("set DRACPU_E2E_MEMORY_POC=true on a dedicated memory POC cluster")
		}
		image = os.Getenv("DRACPU_E2E_TEST_IMAGE")
		gomega.Expect(image).NotTo(gomega.BeEmpty())
		var err error
		fxt, err = fixture.ForGinkgo()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		fxt = fxt.WithPrefix("memory-poc")
		ginkgo.DeferCleanup(fxt.Teardown)
		gomega.Expect(fxt.Setup(ctx)).To(gomega.Succeed())
		node, err = e2enode.PickWorker(ctx, fxt.K8SClientset, time.Second, time.Minute, fxt.Log)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		slices, err := fxt.K8SClientset.ResourceV1().ResourceSlices().List(ctx, metav1.ListOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		candidates = map[int64]map[string]bool{}
		for _, slice := range slices.Items {
			if slice.Spec.Driver != "dra.cpu" || slice.Spec.NodeName == nil || *slice.Spec.NodeName != node.Name {
				continue
			}
			for _, d := range slice.Spec.Devices {
				kind := d.Attributes["dra.cpu/resourceType"].StringValue
				id := d.Attributes["resource.kubernetes.io/numaNode"].IntValue
				if kind == nil || id == nil {
					continue
				}
				if candidates[*id] == nil {
					candidates[*id] = map[string]bool{}
				}
				key := resourceapi.QualifiedName("dra.cpu/size")
				want := resource.MustParse("64Mi")
				switch *kind {
				case "cpu":
					key = "dra.cpu/cpu"
					want = resource.MustParse("2")
				case "hugepages-2m":
					want = resource.MustParse("2Mi")
				case "hugepages-1g":
					want = resource.MustParse("1Gi")
				}
				capacity := d.Capacity[key].Value
				if capacity.Cmp(want) >= 0 {
					candidates[*id][*kind] = true
				}
			}
		}
		if os.Getenv("DRACPU_E2E_MEMORY_POC_CPU_ONLY") == "true" {
			ds, err := fxt.K8SClientset.AppsV1().DaemonSets(daemonSetNamespace).Get(ctx, "dracpu", metav1.GetOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(ds.Spec.Template.Spec.Containers[0].Args).To(gomega.ContainElement("--enable-memory=false"))
		}
	})

	ginkgo.DescribeTable("independent and composed resources", func(ctx context.Context, kinds []string, separate, split bool) {
		if os.Getenv("DRACPU_E2E_MEMORY_POC_CPU_ONLY") == "true" && (len(kinds) != 1 || kinds[0] != "cpu") {
			ginkgo.Skip("CPU-only deployment: run the full POC separately with memory enabled")
		}
		numa, found := pocNUMANode(candidates, kinds)
		if !found && slices.Contains(kinds, "hugepages-1g") {
			ginkgo.Skip("1 GiB capacity is not advertised with the required NUMA alignment on the target node")
		}
		gomega.Expect(found).To(gomega.BeTrue(), "target node must advertise %v on a suitable NUMA node; provision the requested resources before driver startup", kinds)
		claims := []*resourceapi.ResourceClaim{}
		if split && !slices.Contains(kinds, "hugepages-1g") {
			claims = append(claims, pocClaim("memory-pages", []string{"memory", "hugepages-2m"}, numa),
				pocClaim("cpu", []string{"cpu"}, numa))
		} else if split {
			claims = append(claims, pocClaim("hugepages", []string{"hugepages-2m", "hugepages-1g"}, numa),
				pocClaim("cpu", []string{"cpu"}, numa), pocClaim("memory", []string{"memory"}, numa))
		} else if separate {
			for i, kind := range kinds {
				claims = append(claims, pocClaim(fmt.Sprintf("claim-%d", i), []string{kind}, numa))
			}
		} else {
			claims = append(claims, pocClaim("combined", kinds, numa))
		}
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "probe", Namespace: fxt.Namespace.Name}, Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever}}
		e2epod.PinToNode(pod, node.Name)
		expected := [][]string{kinds}
		if split {
			expected = [][]string{{"cpu", "memory", "hugepages-2m"}, {"hugepages-1g"}}
			if !slices.Contains(kinds, "hugepages-1g") {
				expected = [][]string{{"cpu", "memory"}, {"hugepages-2m"}}
			}
		}
		for i, selected := range expected {
			container := corev1.Container{Name: fmt.Sprintf("probe-%d", i), Image: image, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"/dracputester", "--memory-poc"}}
			if slices.Contains(selected, "cpu") {
				container.Env = append(container.Env, corev1.EnvVar{Name: "POC_CPU_COUNT", Value: "1"})
			}
			if len(selected) > 1 || selected[0] != "cpu" {
				container.Env = append(container.Env, corev1.EnvVar{Name: "POC_MEMS", Value: strconv.FormatInt(numa, 10)})
			}
			if slices.Contains(selected, "memory") {
				container.Env = append(container.Env, corev1.EnvVar{Name: "POC_MEMORY_BYTES", Value: "67108864"})
			}
			for _, hp := range []struct{ kind, env, bytes string }{{"hugepages-2m", "POC_HUGE_2M_BYTES", "2097152"}, {"hugepages-1g", "POC_HUGE_1G_BYTES", "1073741824"}} {
				if slices.Contains(kinds, hp.kind) {
					value := "0"
					if slices.Contains(selected, hp.kind) {
						value = hp.bytes
					}
					container.Env = append(container.Env, corev1.EnvVar{Name: hp.env, Value: value})
				}
			}
			for _, claim := range claims {
				if split {
					for _, req := range claim.Spec.Devices.Requests {
						kind := strings.TrimPrefix(req.Exactly.DeviceClassName, "dra.")
						if !slices.Contains(selected, kind) {
							continue
						}
						ref := corev1.ResourceClaim{Name: claim.Name}
						if len(claim.Spec.Devices.Requests) > 1 {
							ref.Request = req.Name
						}
						container.Resources.Claims = append(container.Resources.Claims, ref)
					}
				} else {
					container.Resources.Claims = append(container.Resources.Claims, corev1.ResourceClaim{Name: claim.Name})
				}
			}
			pod.Spec.Containers = append(pod.Spec.Containers, container)
		}
		for _, claim := range claims {
			claim.Namespace = fxt.Namespace.Name
			// Namespace cleanup is registered before the first create, including partial failures.
			_, err := fxt.K8SClientset.ResourceV1().ResourceClaims(claim.Namespace).Create(ctx, claim, metav1.CreateOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			pod.Spec.ResourceClaims = append(pod.Spec.ResourceClaims, corev1.PodResourceClaim{Name: claim.Name, ResourceClaimName: new(claim.Name)})
		}
		created, err := fxt.K8SClientset.CoreV1().Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		gomega.Eventually(func(ctx context.Context) (corev1.PodPhase, error) {
			current, err := fxt.K8SClientset.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
			if err != nil {
				return "", err
			}
			return current.Status.Phase, nil
		}).WithContext(ctx).WithTimeout(3 * time.Minute).WithPolling(time.Second).Should(gomega.Or(gomega.Equal(corev1.PodSucceeded), gomega.Equal(corev1.PodFailed)))
		current, err := fxt.K8SClientset.CoreV1().Pods(pod.Namespace).Get(ctx, pod.Name, metav1.GetOptions{})
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		var exclusive, shared cpuset.CPUSet
		for i, container := range pod.Spec.Containers {
			logs, err := e2epod.GetLogsForContainer(ctx, fxt.K8SClientset, pod.Namespace, pod.Name, container.Name)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(current.Status.Phase).To(gomega.Equal(corev1.PodSucceeded), "%s logs: %s", container.Name, logs)
			var report discovery.MemoryProbe
			gomega.Expect(json.Unmarshal([]byte(logs), &report)).To(gomega.Succeed(), "logs: %s", logs)
			var touched []string
			for _, kind := range expected[i] {
				if kind != "cpu" {
					touched = append(touched, kind)
				}
			}
			gomega.Expect(report.Touched).To(gomega.ConsistOf(touched))
			cpus, err := cpuset.Parse(report.CPUs)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			if slices.Contains(expected[i], "cpu") {
				exclusive = cpus
			} else {
				shared = cpus
			}
		}
		if split {
			gomega.Expect(exclusive.Intersection(shared).IsEmpty()).To(gomega.BeTrue(), "memory-only consumer must use the shared CPU pool")
		}
		gomega.Expect(current.Status.NodeAllocatableResourceClaimStatuses).To(gomega.HaveLen(len(claims)))
		for _, claim := range claims {
			allocated, err := fxt.K8SClientset.ResourceV1().ResourceClaims(claim.Namespace).Get(ctx, claim.Name, metav1.GetOptions{})
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			gomega.Expect(allocated.Status.Allocation).NotTo(gomega.BeNil())
			gomega.Expect(allocated.Status.Allocation.Devices.Results).To(gomega.HaveLen(len(claim.Spec.Devices.Requests)))
			wantNative := map[corev1.ResourceName]resource.Quantity{}
			for _, r := range allocated.Status.Allocation.Devices.Results {
				var kind string
				for _, request := range claim.Spec.Devices.Requests {
					if request.Name == r.Request {
						kind = request.Exactly.DeviceClassName[4:]
					}
				}
				gomega.Expect(kind).NotTo(gomega.BeEmpty())
				expectedPool := node.Name
				if kind != "cpu" {
					expectedPool += "/" + kind
				}
				gomega.Expect(r.Driver).To(gomega.Equal("dra.cpu"))
				gomega.Expect(r.Pool).To(gomega.Equal(expectedPool))
				key := resourceapi.QualifiedName("size")
				native := corev1.ResourceName(kind)
				want := resource.MustParse("64Mi")
				switch kind {
				case "cpu":
					key = "cpu"
					want = resource.MustParse("1")
				case "hugepages-2m":
					native = "hugepages-2Mi"
					want = resource.MustParse("2Mi")
				case "hugepages-1g":
					native = "hugepages-1Gi"
					want = resource.MustParse("1Gi")
				}
				got, ok := r.ConsumedCapacity[key]
				if !ok {
					got = r.ConsumedCapacity[resourceapi.QualifiedName("dra.cpu/"+string(key))]
				}
				gomega.Expect(got.Cmp(want)).To(gomega.BeZero())
				wantNative[native] = want
			}
			var mapping map[corev1.ResourceName]resource.Quantity
			for _, status := range current.Status.NodeAllocatableResourceClaimStatuses {
				if status.ResourceClaimName == claim.Name {
					mapping = map[corev1.ResourceName]resource.Quantity{}
					for _, m := range status.Mapping {
						mapping[m.Name] = *m.Quantity
					}
				}
			}
			gomega.Expect(mapping).To(gomega.HaveLen(len(wantNative)))
			for native, want := range wantNative {
				got := mapping[native]
				gomega.Expect(got.Cmp(want)).To(gomega.BeZero())
			}
		}
		gomega.Expect(e2epod.DeleteSync(ctx, fxt.K8SClientset, created)).To(gomega.Succeed())
	},
		ginkgo.Entry("CPU alone", []string{"cpu"}, false, false),
		ginkgo.Entry("regular memory alone", []string{"memory"}, false, false),
		ginkgo.Entry("2 MiB hugepages alone", ginkgo.Label("memory-poc-2m"), []string{"hugepages-2m"}, false, false),
		ginkgo.Entry("CPU memory and 2 MiB separate claims", ginkgo.Label("memory-poc-2m"), []string{"cpu", "memory", "hugepages-2m"}, true, false),
		ginkgo.Entry("CPU memory and 2 MiB aligned claim", ginkgo.Label("memory-poc-2m"), []string{"cpu", "memory", "hugepages-2m"}, false, false),
		ginkgo.Entry("memory and 2 MiB requests split across containers", ginkgo.Label("memory-poc-2m"), []string{"cpu", "memory", "hugepages-2m"}, false, true),
		ginkgo.Entry("1 GiB hugepages alone", []string{"hugepages-1g"}, false, false),
		ginkgo.Entry("separate claims composed", []string{"cpu", "memory", "hugepages-2m", "hugepages-1g"}, true, false),
		ginkgo.Entry("one aligned mixed claim", []string{"cpu", "memory", "hugepages-2m", "hugepages-1g"}, false, false),
		ginkgo.Entry("disjoint requests in two containers", []string{"cpu", "memory", "hugepages-2m", "hugepages-1g"}, false, true),
	)
})

func pocRequestName(kind string) string {
	switch kind {
	case "hugepages-2m":
		return "hp2m"
	case "hugepages-1g":
		return "hp1g"
	default:
		return kind
	}
}

func pocClaim(name string, kinds []string, numa int64) *resourceapi.ResourceClaim {
	claim := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: name}}
	for _, kind := range kinds {
		key := resourceapi.QualifiedName("dra.cpu/size")
		quantity := resource.MustParse("64Mi")
		switch kind {
		case "cpu":
			key = "dra.cpu/cpu"
			quantity = resource.MustParse("1")
		case "hugepages-2m":
			quantity = resource.MustParse("2Mi")
		case "hugepages-1g":
			quantity = resource.MustParse("1Gi")
		}
		claim.Spec.Devices.Requests = append(claim.Spec.Devices.Requests, resourceapi.DeviceRequest{Name: pocRequestName(kind), Exactly: &resourceapi.ExactDeviceRequest{DeviceClassName: "dra." + kind, Count: 1, Capacity: &resourceapi.CapacityRequirements{Requests: map[resourceapi.QualifiedName]resource.Quantity{key: quantity}}, Selectors: []resourceapi.DeviceSelector{{CEL: &resourceapi.CELDeviceSelector{Expression: fmt.Sprintf(`device.attributes["resource.kubernetes.io"].numaNode == %d`, numa)}}}}})
	}
	if len(kinds) > 1 {
		claim.Spec.Devices.Constraints = []resourceapi.DeviceConstraint{{MatchAttribute: new(resourceapi.FullyQualifiedName("resource.kubernetes.io/numaNode"))}}
	}
	return claim
}

func pocNUMANode(candidates map[int64]map[string]bool, kinds []string) (int64, bool) {
	var selected int64
	found := false
	for id, available := range candidates {
		if !slices.ContainsFunc(kinds, func(kind string) bool { return !available[kind] }) && (!found || id < selected) {
			selected, found = id, true
		}
	}
	return selected, found
}
