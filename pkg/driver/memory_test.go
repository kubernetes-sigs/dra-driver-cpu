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
package driver

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/containerd/nri/pkg/api"
	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
	"github.com/stretchr/testify/require"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/utils/cpuset"
)

func memoryFixture(t *testing.T, dir string, enabled bool) *CPUDriver {
	t.Helper()
	infos := mockCPUInfos_SingleSocket_4CPUS_HT
	fs := testSysFS(infos)
	for name, data := range map[string]string{
		"online": "0-1", "node0/meminfo": "Node 0 MemTotal: 8388608 kB", "node1/meminfo": "Node 1 MemTotal: 1048576 kB",
		"node0/hugepages/hugepages-2048kB/nr_hugepages": "4", "node0/hugepages/hugepages-1048576kB/nr_hugepages": "2",
	} {
		fs["devices/system/node/"+name] = &fstest.MapFile{Data: []byte(data)}
	}
	cp, err := New(testr.New(t), Providers{CPUInfo: &cpuinfo.MockCPUInfoProvider{CPUInfos: infos}, SysFS: fs}, &Config{DriverName: "dra.cpu", NodeName: "worker", EnableMemory: enabled, CPUDeviceMode: device.CPU_DEVICE_MODE_GROUPED, CPUDeviceGroupBy: device.GROUP_BY_NUMA_NODE, PublishNodeAllocatableResourceMapping: true})
	require.NoError(t, err)
	mgr, err := NewCdiManager(testr.New(t), "dra.cpu", dir)
	require.NoError(t, err)
	cp.cdiMgr = mgr
	_, err = cp.Synchronize(t.Context(), nil, nil)
	require.NoError(t, err)
	return cp
}

func memoryClaim(uid string, requests ...string) *resourceapi.ResourceClaim {
	claim := &resourceapi.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: uid, Namespace: "default", UID: types.UID(uid)}}
	claim.Status.Allocation = &resourceapi.AllocationResult{}
	for _, req := range requests {
		r := resourceapi.DeviceRequestAllocationResult{Driver: "dra.cpu", Request: req, Pool: "worker"}
		switch req {
		case "cpu":
			r.Device = "cpudevnuma000"
			r.ConsumedCapacity = map[resourceapi.QualifiedName]resource.Quantity{"cpu": resource.MustParse("1")}
		case "memory", "other-memory":
			r.Pool += "/memory"
			r.Device = "memory-numa0"
			r.ConsumedCapacity = map[resourceapi.QualifiedName]resource.Quantity{"size": resource.MustParse("64Mi")}
		case "remote":
			r.Pool += "/memory"
			r.Device = "memory-numa1"
			r.ConsumedCapacity = map[resourceapi.QualifiedName]resource.Quantity{"size": resource.MustParse("32Mi")}
		case "hp2m":
			r.Pool += "/hugepages-2m"
			r.Device = "hugepages-2m-numa0"
			r.ConsumedCapacity = map[resourceapi.QualifiedName]resource.Quantity{"size": resource.MustParse("2Mi")}
		case "hp1g":
			r.Pool += "/hugepages-1g"
			r.Device = "hugepages-1g-numa0"
			r.ConsumedCapacity = map[resourceapi.QualifiedName]resource.Quantity{"size": resource.MustParse("1Gi")}
		default:
			panic(req)
		}
		claim.Status.Allocation.Devices.Results = append(claim.Status.Allocation.Devices.Results, r)
	}
	return claim
}

func prepareMemoryClaim(t *testing.T, cp *CPUDriver, claim *resourceapi.ResourceClaim) []kubeletplugin.Device {
	t.Helper()
	results, err := cp.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.NoError(t, results[claim.UID].Err)
	return results[claim.UID].Devices
}

func requestEnvs(t *testing.T, cp *CPUDriver, devices []kubeletplugin.Device, requests ...string) []string {
	t.Helper()
	require.NoError(t, cp.cdiMgr.Refresh())
	var envs []string
	seen := map[string]bool{}
	for _, d := range devices {
		if len(requests) > 0 && (len(d.Requests) == 0 || !slices.Contains(requests, d.Requests[0])) {
			continue
		}
		for _, id := range d.CDIDeviceIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			_, name, _ := strings.Cut(id, "=")
			data, err := cp.cdiMgr.GetDeviceEnv(name)
			require.NoError(t, err)
			envs = append(envs, data...)
		}
	}
	return envs
}

func TestMemoryCombinations(t *testing.T) {
	kinds := []string{"cpu", "memory", "hp2m", "hp1g"}
	for bits := 1; bits < 16; bits++ {
		var requests []string
		for i, kind := range kinds {
			if bits&(1<<i) != 0 {
				requests = append(requests, kind)
			}
		}
		t.Run(strings.Join(requests, "+"), func(t *testing.T) {
			cp := memoryFixture(t, t.TempDir(), true)
			claim := memoryClaim("combined", requests...)
			devices := prepareMemoryClaim(t, cp, claim)
			require.Len(t, devices, len(requests))
			require.Equal(t, devices, prepareMemoryClaim(t, cp, claim))
			ctr := &api.Container{Name: "app", Id: "app-id", Env: requestEnvs(t, cp, devices)}
			adjust, _, err := cp.CreateContainer(t.Context(), &api.PodSandbox{Uid: "pod"}, ctr)
			require.NoError(t, err)
			cpus, err := cpuset.Parse(adjust.Linux.Resources.Cpu.Cpus)
			require.NoError(t, err)
			if slices.Contains(requests, "cpu") {
				require.Equal(t, 1, cpus.Size())
			} else {
				require.Equal(t, "0-3", cpus.String())
			}
			if slices.Contains(requests, "memory") {
				require.Equal(t, int64(64<<20), adjust.Linux.Resources.Memory.Limit.Value)
			}
			if bits > 1 {
				require.Equal(t, "0", adjust.Linux.Resources.Cpu.Mems)
			}
			huge := map[string]uint64{}
			for _, limit := range adjust.Linux.Resources.HugepageLimits {
				huge[limit.PageSize] = limit.Limit
			}
			if slices.Contains(requests, "hp2m") {
				require.Equal(t, uint64(2<<20), huge["2MB"])
			}
			if slices.Contains(requests, "hp1g") {
				require.Equal(t, uint64(1<<30), huge["1GB"])
			}
			released, err := cp.UnprepareResourceClaims(t.Context(), []kubeletplugin.NamespacedObject{{UID: claim.UID}})
			require.NoError(t, err)
			require.NoError(t, released[claim.UID])
			require.Empty(t, cp.composedClaims)
			require.Equal(t, "0-3", cp.allocationState.GetSharedCPUs().String())
		})
	}
}

func TestMemoryRequestScoping(t *testing.T) {
	cp := memoryFixture(t, t.TempDir(), true)
	devices := prepareMemoryClaim(t, cp, memoryClaim("mixed", "cpu", "memory", "hp2m", "hp1g"))
	pod := &api.PodSandbox{Uid: "pod", Id: "sandbox"}
	for _, tc := range []struct {
		name       string
		requests   []string
		memory     int64
		hp2m, hp1g uint64
		cpus       int
	}{
		{"a", []string{"cpu", "memory", "hp2m"}, 64 << 20, 2 << 20, 0, 1},
		{"b", []string{"hp1g"}, 0, 0, 1 << 30, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envs := requestEnvs(t, cp, devices, tc.requests...)
			envs = append(envs, envs...) // Repeated CDI references must not double the budget.
			ctr := &api.Container{Name: tc.name, Id: tc.name, Env: envs, PodSandboxId: pod.Id}
			adjust, _, err := cp.CreateContainer(t.Context(), pod, ctr)
			require.NoError(t, err)
			cpus, err := cpuset.Parse(adjust.Linux.Resources.Cpu.Cpus)
			require.NoError(t, err)
			require.Equal(t, tc.cpus, cpus.Size())
			require.Equal(t, "0", adjust.Linux.Resources.Cpu.Mems)
			if tc.memory > 0 {
				require.Equal(t, tc.memory, adjust.Linux.Resources.Memory.Limit.Value)
			} else {
				require.Nil(t, adjust.Linux.Resources.Memory)
			}
			huge := map[string]uint64{}
			for _, h := range adjust.Linux.Resources.HugepageLimits {
				huge[h.PageSize] = h.Limit
			}
			require.Equal(t, map[string]uint64{"2MB": tc.hp2m, "1GB": tc.hp1g}, huge)
			_, _, err = cp.CreateContainer(t.Context(), pod, ctr)
			require.NoError(t, err)
		})
	}
	_, _, err := cp.CreateContainer(t.Context(), pod, &api.Container{Name: "thief", Id: "thief", Env: requestEnvs(t, cp, devices, "memory")})
	require.ErrorContains(t, err, "belongs to container")
	require.Contains(t, cp.podState.GetContainersWithSharedCPUs(), types.UID("b"))
}

func TestMemoryAcrossClaimsAndNUMA(t *testing.T) {
	cp := memoryFixture(t, t.TempDir(), true)
	envs := requestEnvs(t, cp, prepareMemoryClaim(t, cp, memoryClaim("first", "memory", "hp2m", "hp1g")), "memory", "hp2m")
	envs = append(envs, requestEnvs(t, cp, prepareMemoryClaim(t, cp, memoryClaim("second", "remote", "hp1g")))...)
	envs = append(envs, requestEnvs(t, cp, prepareMemoryClaim(t, cp, memoryClaim("legacy-cpu", "cpu")))...)
	adjust, _, err := cp.CreateContainer(t.Context(), &api.PodSandbox{Uid: "pod"}, &api.Container{Name: "app", Id: "app", Env: envs})
	require.NoError(t, err)
	require.Equal(t, "0-1", adjust.Linux.Resources.Cpu.Mems)
	cpus, err := cpuset.Parse(adjust.Linux.Resources.Cpu.Cpus)
	require.NoError(t, err)
	require.Equal(t, 1, cpus.Size())
	require.Equal(t, int64(96<<20), adjust.Linux.Resources.Memory.Limit.Value)
	huge := map[string]uint64{}
	for _, h := range adjust.Linux.Resources.HugepageLimits {
		huge[h.PageSize] = h.Limit
	}
	require.Equal(t, map[string]uint64{"2MB": 2 << 20, "1GB": 1 << 30}, huge)
}

func TestMemoryPreparationBoundary(t *testing.T) {
	for _, tc := range []struct {
		name    string
		enabled bool
		change  func(*resourceapi.ResourceClaim)
		want    string
	}{
		{"disabled", false, func(c *resourceapi.ResourceClaim) {}, "unavailable"},
		{"wrong pool", true, func(c *resourceapi.ResourceClaim) { c.Status.Allocation.Devices.Results[0].Pool = "elsewhere" }, "unavailable"},
		{"missing size", true, func(c *resourceapi.ResourceClaim) { c.Status.Allocation.Devices.Results[0].ConsumedCapacity = nil }, "positive"},
		{"admin", true, func(c *resourceapi.ResourceClaim) { c.Status.Allocation.Devices.Results[0].AdminAccess = new(true) }, "admin access"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cp := memoryFixture(t, t.TempDir(), tc.enabled)
			claim := memoryClaim("invalid", "memory")
			tc.change(claim)
			result, err := cp.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
			require.NoError(t, err)
			require.ErrorContains(t, result[claim.UID].Err, tc.want)
			require.Empty(t, cp.composedClaims)
			require.Equal(t, "0-3", cp.allocationState.GetSharedCPUs().String())
		})
	}
}

func TestMemoryCDIFailureRollback(t *testing.T) {
	cp := memoryFixture(t, t.TempDir(), true)
	file := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(file, []byte("file"), 0600))
	mgr, err := NewCdiManager(testr.New(t), "dra.cpu", file)
	require.NoError(t, err)
	cp.cdiMgr = mgr
	claim := memoryClaim("rollback", "cpu", "memory")
	result, err := cp.PrepareResourceClaims(t.Context(), []*resourceapi.ResourceClaim{claim})
	require.NoError(t, err)
	require.Error(t, result[claim.UID].Err)
	require.Empty(t, cp.composedClaims)
	require.Equal(t, "0-3", cp.allocationState.GetSharedCPUs().String())
}

func TestMemorySynchronization(t *testing.T) {
	dir := t.TempDir()
	first := memoryFixture(t, dir, true)
	devices := prepareMemoryClaim(t, first, memoryClaim("restart", "cpu", "memory", "hp1g"))
	pod := &api.PodSandbox{Uid: "pod", Id: "sandbox"}
	containers := []*api.Container{
		{Name: "cpu", Id: "cpu", PodSandboxId: pod.Id, Env: requestEnvs(t, first, devices, "cpu")},
		{Name: "memory", Id: "memory", PodSandboxId: pod.Id, Env: requestEnvs(t, first, devices, "memory", "hp1g")},
	}
	// New() starts unsynchronized; the fixture's empty sync is undone to model a cold process.
	cold := memoryFixture(t, dir, true)
	cold.hasSynchronized = false
	cold.initialSync = nil
	updates, err := cold.Synchronize(t.Context(), []*api.PodSandbox{pod}, containers)
	require.NoError(t, err)
	require.NotEmpty(t, updates)
	ids := make(map[string]bool)
	for _, update := range updates {
		require.False(t, ids[update.ContainerId], "one combined update per container")
		ids[update.ContainerId] = true
	}
	require.Equal(t, 3, cold.allocationState.GetSharedCPUs().Size())
	for _, ctr := range containers {
		_, _, err := cold.CreateContainer(t.Context(), pod, ctr)
		require.NoError(t, err)
	}
	require.Equal(t, devices, prepareMemoryClaim(t, cold, memoryClaim("restart", "cpu", "memory", "hp1g")))
	_, err = cold.Synchronize(t.Context(), []*api.PodSandbox{pod}, containers)
	require.NoError(t, err)
}

func TestMemoryPublication(t *testing.T) {
	cp := memoryFixture(t, t.TempDir(), true)
	publisher := &mockKubeletPlugin{}
	cp.draPlugin = publisher
	cp.PublishResources(t.Context())
	require.Len(t, publisher.publishedResources.Pools, 4)
	for name, pool := range publisher.publishedResources.Pools {
		for _, slice := range pool.Slices {
			for _, d := range slice.Devices {
				require.NotNil(t, d.Attributes["dra.cpu/resourceType"].StringValue)
				if name != "worker" {
					require.Len(t, d.NodeAllocatableResources, 1)
				}
			}
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	reports := make(chan kubeletplugin.DeviceHealthReport, 1)
	done := make(chan error, 1)
	go func() { done <- cp.WatchHealthStatus(ctx, reports) }()
	report := <-reports
	for _, d := range report.Devices {
		if strings.HasPrefix(d.DeviceName, "cpudev") {
			require.Equal(t, "worker", d.PoolName)
		} else {
			require.Contains(t, []string{"worker/memory", "worker/hugepages-2m", "worker/hugepages-1g"}, d.PoolName)
		}
	}
	cancel()
	require.NoError(t, <-done)
}

func TestMemoryMalformedPayload(t *testing.T) {
	cp := memoryFixture(t, t.TempDir(), true)
	for i, env := range []string{"DRA_RESOURCE_fake", "DRA_RESOURCE_fake={}", `DRA_RESOURCE_fake={"version":2,"claimUID":"a","request":"b"}`} {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			_, _, err := cp.CreateContainer(t.Context(), &api.PodSandbox{Uid: "pod"}, &api.Container{Name: "app", Env: []string{env}})
			require.Error(t, err)
		})
	}
}

func TestComposedClaimRejectsLegacyInjection(t *testing.T) {
	cp := memoryFixture(t, t.TempDir(), true)
	devices := prepareMemoryClaim(t, cp, memoryClaim("mixed", "cpu", "memory"))
	envs := requestEnvs(t, cp, devices, "cpu")
	require.NotEmpty(t, envs)
	held, ok := cp.allocationState.GetResourceClaimAllocation("mixed")
	require.True(t, ok)
	_, _, err := cp.CreateContainer(t.Context(), &api.PodSandbox{Uid: "pod"}, &api.Container{Name: "app", Env: []string{"DRA_CPUSET_mixed=" + held.String()}})
	require.ErrorContains(t, err, "request-scoped")
}
