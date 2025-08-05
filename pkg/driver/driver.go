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
	"time"

	"github.com/containerd/nri/pkg/stub"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/dynamic-resource-allocation/kubeletplugin"
	"k8s.io/klog/v2"
)

const (
	kubeletPluginPath = "/var/lib/kubelet/plugins"
	// maxAttempts indicates the number of times the driver will try to recover itself before failing
	maxAttempts = 5
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

	driverPluginPath := filepath.Join(kubeletPluginPath, driverName)
	if err := os.MkdirAll(driverPluginPath, 0750); err != nil {
		return nil, fmt.Errorf("failed to create plugin path %s: %w", driverPluginPath, err)
	}

	kubeletOpts := []kubeletplugin.Option{
		kubeletplugin.DriverName(driverName),
		kubeletplugin.NodeName(nodeName),
		kubeletplugin.KubeClient(kubeClient),
	}
	d, err := kubeletplugin.Start(ctx, plugin, kubeletOpts...)
	if err != nil {
		return nil, fmt.Errorf("start kubelet plugin: %w", err)
	}
	plugin.draPlugin = d
	err = wait.PollUntilContextTimeout(ctx, 1*time.Second, 30*time.Second, true, func(context.Context) (bool, error) {
		status := plugin.draPlugin.RegistrationStatus()
		if status == nil {
			return false, nil
		}
		return status.PluginRegistered, nil
	})
	if err != nil {
		return nil, err
	}

	cdiMgr, err := NewCdiManager(driverName)
	if err != nil {
		return nil, fmt.Errorf("failed to create CDI manager: %w", err)
	}
	plugin.cdiMgr = cdiMgr

	// register the NRI plugin
	nriOpts := []stub.Option{
		stub.WithPluginName(driverName),
		stub.WithPluginIdx("00"),
		// https://github.com/containerd/nri/pull/173
		// Otherwise it silently exits the program
		stub.WithOnClose(func() {
			klog.Infof("%s NRI plugin closed", driverName)
		}),
	}
	stub, err := stub.New(plugin, nriOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create plugin stub: %w", err)
	}
	plugin.nriPlugin = stub

	go func() {
		for i := 0; i < maxAttempts; i++ {
			err = plugin.nriPlugin.Run(ctx)
			if err != nil {
				klog.Infof("NRI plugin failed with error %v", err)
			}
			select {
			case <-ctx.Done():
				return
			default:
				klog.Infof("Restarting NRI plugin %d out of %d", i, maxAttempts)
			}
		}
		klog.Fatalf("NRI plugin failed for %d times to be restarted", maxAttempts)
	}()

	// publish available resources
	go plugin.PublishResources(ctx)

	return plugin, nil
}

// Stop stops the CPUDriver.
func (cp *CPUDriver) Stop() {
	cp.nriPlugin.Stop()
	cp.draPlugin.Stop()
}

// Shutdown is called when the runtime is shutting down.
func (cp *CPUDriver) Shutdown(_ context.Context) {
	klog.Info("Runtime shutting down...")
}
