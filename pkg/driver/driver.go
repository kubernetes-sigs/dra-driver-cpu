/*
Copyright 2024 The Kubernetes Authors.

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

	"github.com/containerd/nri/pkg/api"
	"github.com/containerd/nri/pkg/stub"
	"k8s.io/client-go/kubernetes"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
)

// CPUDriver is the structure that holds all the driver runtime information.
type CPUDriver struct {
	driverName        string
	nodeName          string
	kubeClient        kubernetes.Interface
	draPlugin         *kubeletplugin.Helper
	nriPlugin         stub.Stub
	podConfigStore    *PodConfigStore
	cdiMgr            *CdiManager
	cpuIDToDeviceName map[int]string
	deviceNameToCPUID map[string]int
}

// Start creates and starts a new CPUDriver.
func Start(ctx context.Context, driverName string, kubeClient kubernetes.Interface, nodeName string) (*CPUDriver, error) {
	plugin := &CPUDriver{
		driverName:        driverName,
		nodeName:          nodeName,
		kubeClient:        kubeClient,
		podConfigStore:    NewPodConfigStore(),
		cpuIDToDeviceName: make(map[int]string),
		deviceNameToCPUID: make(map[string]int),
	}

	driverPluginPath := filepath.Join("/var/lib/kubelet/plugins", driverName)
	if err := os.MkdirAll(driverPluginPath, 0750); err != nil {
		return nil, fmt.Errorf("failed to create plugin path %s: %w", driverPluginPath, err)
	}

	d, err := kubeletplugin.Start(ctx, plugin, kubeletplugin.DriverName(driverName))
	if err != nil {
		return nil, fmt.Errorf("start kubelet plugin: %w", err)
	}
	plugin.draPlugin = d

	cdiMgr, err := NewCdiManager(driverName)
	if err != nil {
		return nil, fmt.Errorf("failed to create CDI manager: %w", err)
	}
	plugin.cdiMgr = cdiMgr

	// publish available resources
	go plugin.PublishResources(ctx)

	return plugin, nil
}

// Stop stops the CPUDriver.
func (cp *CPUDriver) Stop() {
	cp.draPlugin.Stop()
}



// nri_hooks.go stubs
func (cp *CPUDriver) Synchronize(ctx context.Context, pods []*api.PodSandbox, containers []*api.Container) ([]*api.ContainerUpdate, error) {
	return nil, nil
}
func (cp *CPUDriver) CreateContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) (*api.ContainerAdjustment, []*api.ContainerUpdate, error) {
	return nil, nil, nil
}
func (cp *CPUDriver) RemoveContainer(ctx context.Context, pod *api.PodSandbox, ctr *api.Container) error {
	return nil
}
func (cp *CPUDriver) RunPodSandbox(ctx context.Context, pod *api.PodSandbox) error {
	return nil
}
func (cp *CPUDriver) StopPodSandbox(ctx context.Context, pod *api.PodSandbox) error {
	return nil
}
func (cp *CPUDriver) RemovePodSandbox(ctx context.Context, pod *api.PodSandbox) error {
	return nil
}
func (cp *CPUDriver) Shutdown(ctx context.Context) {}