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

package driverconfig

import "github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"

// DefaultKubeletRootDir is the standard kubelet root directory, so behaviour is
// unchanged unless the kubelet --root-dir is relocated.
const DefaultKubeletRootDir = "/var/lib/kubelet"

// Default returns a Config with built-in default values.
func Default() Config {
	return Config{
		BindAddress:    ":8080",
		CPUDeviceMode:  device.CPU_DEVICE_MODE_GROUPED,
		GroupBy:        device.GROUP_BY_NUMA_NODE,
		KubeletRootDir: DefaultKubeletRootDir,
		Allocator:      AllocatorCPUManager,
	}
}
