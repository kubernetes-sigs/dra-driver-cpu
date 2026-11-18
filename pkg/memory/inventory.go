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
package memory

import (
	"fmt"
	"io/fs"
	"math"
	"os"
	"path"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
	"k8s.io/utils/cpuset"
)

const (
	ResourceType resourceapi.QualifiedName = "dra.cpu/resourceType"
	Size         resourceapi.QualifiedName = "dra.cpu/size"
	Regular                                = "memory"
	Huge2M                                 = "hugepages-2m"
	Huge1G                                 = "hugepages-1g"
)

type Device struct {
	Kind     string
	NUMA     int
	Bytes    int64
	PageSize int64
}

func (d Device) Name() string            { return fmt.Sprintf("%s-numa%d", d.Kind, d.NUMA) }
func (d Device) Pool(node string) string { return node + "/" + d.Kind }

func (d Device) Resource() resourceapi.Device {
	q := *resource.NewQuantity(d.Bytes, resource.BinarySI)
	page := *resource.NewQuantity(d.PageSize, resource.BinarySI)
	policy := &resourceapi.CapacityRequestPolicy{Default: &page}
	// A one-page device cannot satisfy the API's min+step <= capacity rule.
	if d.Bytes == d.PageSize {
		policy.ValidValues = []resource.Quantity{page}
	} else {
		policy.ValidRange = &resourceapi.CapacityRequestPolicyRange{Min: &page, Step: &page}
	}
	native := corev1.ResourceMemory
	if d.Kind == Huge2M {
		native = "hugepages-2Mi"
	}
	if d.Kind == Huge1G {
		native = "hugepages-1Gi"
	}
	return resourceapi.Device{
		Name: d.Name(),
		Attributes: map[resourceapi.QualifiedName]resourceapi.DeviceAttribute{
			ResourceType: {StringValue: new(d.Kind)},
			deviceattribute.StandardDeviceAttributeNUMANode: {IntValue: new(int64(d.NUMA))},
		},
		Capacity:                 map[resourceapi.QualifiedName]resourceapi.DeviceCapacity{Size: {Value: q, RequestPolicy: policy}},
		AllowMultipleAllocations: new(true),
		NodeAllocatableResources: map[corev1.ResourceName]resourceapi.NodeAllocatableResource{
			native: {Mapping: &resourceapi.NodeAllocatableMapping{CapacityKey: new(Size), CapacityMultiplier: new(resource.MustParse("1"))}},
		},
	}
}

// Discover takes a fixed inventory snapshot; free memory is not DRA capacity.
func Discover(sfs fs.FS) ([]Device, error) {
	online, err := fs.ReadFile(sfs, "devices/system/node/online")
	if err != nil {
		return nil, fmt.Errorf("read NUMA online nodes; expose readable node sysfs or use --enable-memory=false: %w", err)
	}
	nodes, err := cpuset.Parse(strings.TrimSpace(string(online)))
	if err != nil {
		return nil, fmt.Errorf("parse NUMA online nodes %q; provide a Linux node list: %w", online, err)
	}
	if nodes.IsEmpty() {
		return nil, fmt.Errorf("NUMA online node list is empty; expose online memory nodes or use --enable-memory=false")
	}
	var devices []Device
	for _, node := range nodes.List() {
		root := fmt.Sprintf("devices/system/node/node%d", node)
		data, err := fs.ReadFile(sfs, path.Join(root, "meminfo"))
		if err != nil {
			return nil, fmt.Errorf("read %s/meminfo; expose NUMA memory inventory: %w", root, err)
		}
		total, err := memTotal(string(data), node)
		if err != nil {
			return nil, fmt.Errorf("parse %s/meminfo: %w", root, err)
		}
		entries, err := fs.ReadDir(sfs, path.Join(root, "hugepages"))
		if err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("read %s/hugepages: %w", root, err)
		}
		reserved := int64(0)
		var huge []Device
		for _, entry := range entries {
			sizeText, ok := strings.CutPrefix(entry.Name(), "hugepages-")
			if !ok {
				continue
			}
			sizeText, ok = strings.CutSuffix(sizeText, "kB")
			if !ok {
				return nil, fmt.Errorf("invalid hugepage directory %s/%s; use kernel-provided sysfs", root, entry.Name())
			}
			sizeKB, err := strconv.ParseInt(sizeText, 10, 64)
			if err != nil || sizeKB <= 0 || sizeKB > math.MaxInt64/1024 {
				return nil, fmt.Errorf("invalid hugepage size %q; use kernel-provided sysfs", sizeText)
			}
			size := sizeKB * 1024
			file := path.Join(root, "hugepages", entry.Name(), "nr_hugepages")
			countData, err := fs.ReadFile(sfs, file)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", file, err)
			}
			count, err := strconv.ParseInt(strings.TrimSpace(string(countData)), 10, 64)
			if err != nil || count < 0 || count > (math.MaxInt64-reserved)/size {
				return nil, fmt.Errorf("invalid page count in %s; provide a nonnegative count without byte overflow", file)
			}
			bytes := count * size
			reserved += bytes
			kind := ""
			switch size {
			case 2 << 20:
				kind = Huge2M
			case 1 << 30:
				kind = Huge1G
			}
			if kind != "" && bytes > 0 {
				huge = append(huge, Device{Kind: kind, NUMA: node, Bytes: bytes, PageSize: size})
			}
		}
		if reserved > total {
			return nil, fmt.Errorf("node%d hugetlb capacity %d exceeds MemTotal %d; correct the sysfs inventory", node, reserved, total)
		}
		// All hugetlb sizes consume ordinary memory, including sizes not exposed here.
		page := int64(os.Getpagesize())
		bytes := (total - reserved) / page * page
		if bytes > 0 {
			devices = append(devices, Device{Kind: Regular, NUMA: node, Bytes: bytes, PageSize: page})
		}
		devices = append(devices, huge...)
	}
	return devices, nil
}

func memTotal(data string, node int) (int64, error) {
	prefix := fmt.Sprintf("Node %d MemTotal:", node)
	for line := range strings.SplitSeq(data, "\n") {
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), prefix); ok {
			fields := strings.Fields(rest)
			if len(fields) != 2 || fields[1] != "kB" {
				break
			}
			kb, err := strconv.ParseInt(fields[0], 10, 64)
			if err != nil || kb < 0 || kb > math.MaxInt64/1024 {
				break
			}
			return kb * 1024, nil
		}
	}
	return 0, fmt.Errorf("missing or invalid %q in kB; provide kernel NUMA meminfo", prefix)
}

func Consumed(result resourceapi.DeviceRequestAllocationResult, driver string, d Device) (int64, error) {
	q, ok := result.ConsumedCapacity[Size]
	if !ok {
		q, ok = result.ConsumedCapacity["size"]
	}
	if !ok {
		q, ok = result.ConsumedCapacity[resourceapi.QualifiedName(driver+"/size")]
	}
	bytes, exact := q.AsInt64()
	if !ok || !exact || bytes <= 0 || bytes > d.Bytes || bytes%d.PageSize != 0 {
		return 0, fmt.Errorf("device %q needs a positive whole-byte size up to %d, aligned to %d bytes; correct the claim allocation", result.Device, d.Bytes, d.PageSize)
	}
	return bytes, nil
}
