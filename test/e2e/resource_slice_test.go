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

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/driver"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/dynamic-resource-allocation/deviceattribute"
)

var _ = ginkgo.Describe("Resource Attributes", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		fxt           *fixture.Fixture
		cpuDeviceMode string
		groupBy       string
		slices        []resourcev1.ResourceSlice
	)

	ginkgo.BeforeAll(func(ctx context.Context) {
		var err error
		fxt, err = fixture.ForGinkgo()
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create fixture")

		ginkgo.By("reading daemonset configuration")
		daemonSet, err := fxt.K8SClientset.AppsV1().DaemonSets(daemonSetNamespace).Get(ctx, "dracpu", metav1.GetOptions{})
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get dracpu daemonset")
		gomega.Expect(daemonSet.Spec.Template.Spec.Containers).ToNot(gomega.BeEmpty())

		cnt := &daemonSet.Spec.Template.Spec.Containers[0]
		if val, ok := findArgInContainer(cnt, argCPUDeviceMode); ok {
			cpuDeviceMode = val
		}
		if val, ok := findArgInContainer(cnt, argGroupBy); ok {
			groupBy = val
		}
		fxt.Log.Info("daemonset configuration", "cpuDeviceMode", cpuDeviceMode, "groupBy", groupBy)

		ginkgo.By("listing ResourceSlices for driver " + driverName)
		sliceList, err := fxt.K8SClientset.ResourceV1().ResourceSlices().List(ctx, metav1.ListOptions{
			FieldSelector: "spec.driver=" + driverName,
		})
		gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot list ResourceSlices")
		gomega.Expect(sliceList.Items).ToNot(gomega.BeEmpty(), "no ResourceSlices found for driver %s", driverName)
		slices = sliceList.Items
		fxt.Log.Info("found ResourceSlices", "count", len(slices))
	})

	ginkgo.It("should have devices in ResourceSlices", func() {
		totalDevices := 0
		for _, slice := range slices {
			totalDevices += len(slice.Spec.Devices)
		}
		gomega.Expect(totalDevices).To(gomega.BeNumerically(">", 0), "no devices found across all ResourceSlices")
	})

	ginkgo.It("should have correct base attributes on every device", func() {
		type attrCheck struct {
			name    resourcev1.QualifiedName
			checker func(resourcev1.DeviceAttribute) bool
		}

		isInt := func(a resourcev1.DeviceAttribute) bool { return a.IntValue != nil }
		isBool := func(a resourcev1.DeviceAttribute) bool { return a.BoolValue != nil }
		isString := func(a resourcev1.DeviceAttribute) bool { return a.StringValue != nil }

		var checks []attrCheck
		switch cpuDeviceMode {
		case driver.CPU_DEVICE_MODE_INDIVIDUAL:
			checks = []attrCheck{
				{driver.AttributeNUMANodeID, isInt},
				{driver.AttributeSocketID, isInt},
				{driver.AttributeSMTEnabled, isBool},
				{driver.AttributeCacheL3ID, isInt},
				{driver.AttributeCoreType, isString},
				{driver.AttributeCoreID, isInt},
				{driver.AttributeCPUID, isInt},
			}
		default:
			switch groupBy {
			case driver.GROUP_BY_MACHINE:
				checks = []attrCheck{
					{driver.AttributeSMTEnabled, isBool},
					{driver.AttributeNumCPUs, isInt},
				}
			case driver.GROUP_BY_NUMA_NODE:
				checks = []attrCheck{
					{driver.AttributeNUMANodeID, isInt},
					{driver.AttributeSocketID, isInt},
					{driver.AttributeSMTEnabled, isBool},
					{driver.AttributeNumCPUs, isInt},
				}
			case driver.GROUP_BY_SOCKET:
				checks = []attrCheck{
					{driver.AttributeSocketID, isInt},
					{driver.AttributeSMTEnabled, isBool},
					{driver.AttributeNumCPUs, isInt},
				}
			default:
				ginkgo.Fail("unknown CPU device group-by configuration: " + groupBy)
			}
		}

		for _, slice := range slices {
			for _, dev := range slice.Spec.Devices {
				for _, check := range checks {
					attr, ok := dev.Attributes[check.name]
					gomega.Expect(ok).To(gomega.BeTrue(),
						"device %q in slice %q missing attribute %s", dev.Name, slice.Name, check.name)
					gomega.Expect(check.checker(attr)).To(gomega.BeTrue(),
						"device %q in slice %q attribute %s has wrong type", dev.Name, slice.Name, check.name)
				}
			}
		}
	})

	ginkgo.It("should have valid PCIe root attributes when present", func() {
		devicesWithPCIeRoots := 0

		for _, slice := range slices {
			for _, dev := range slice.Spec.Devices {
				_, hasPCIeRoots := dev.Attributes[deviceattribute.StandardDeviceAttributePCIeRoot]
				if !hasPCIeRoots {
					continue
				}
				devicesWithPCIeRoots++
			}
		}

		if devicesWithPCIeRoots == 0 {
			// Skip is more explicit than passing the tests doing nothing.
			// It's the strongest signal we get until we find a way to inspect the system before
			// the suite runs and set expectations accordingly.
			ginkgo.Skip("No PCIe roots reported on this system")
		}

		for _, slice := range slices {
			for _, dev := range slice.Spec.Devices {
				pcieRoots, ok := dev.Attributes[deviceattribute.StandardDeviceAttributePCIeRoot]
				if !ok {
					continue
				}
				gomega.Expect(pcieRoots.StringValues).ToNot(gomega.BeEmpty(),
					"device %q in slice %q has resource.kubernetes.io/pcieRoot but StringValues is empty", dev.Name, slice.Name)
			}
		}
	})
})
