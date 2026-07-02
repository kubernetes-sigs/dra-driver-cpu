/*
Copyright 2026 The Kubernetes Authors.

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

package cpuinfo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/sysfs"
)

func TestSystemCPUInfoUsesSysFSOverlay(t *testing.T) {
	tmpDir := t.TempDir()
	createFakeCPUTopology(t, tmpDir, fakeCPUTopology{
		numSockets:            1,
		numNumaNodesPerSocket: 1,
		numCoresPerNumaNode:   2,
		cpusPerCore:           1,
		coresPerL3:            2,
	})

	base := os.DirFS(filepath.Join(tmpDir, "sys")).(sysfs.FS)
	sysfs, err := device.NewOverlaySysFS(base, map[string]string{
		"/sys/devices/system/cpu/online":                            "0\n",
		"/sys/devices/system/cpu/cpu0/topology/physical_package_id": "7\n",
	})
	if err != nil {
		t.Fatalf("NewOverlaySysFS() error = %v", err)
	}

	provider := NewSystemCPUInfoFromFS(sysfs)
	infos, err := provider.GetCPUInfos(testr.New(t))
	if err != nil {
		t.Fatalf("GetCPUInfos() error = %v", err)
	}
	if got, want := len(infos), 1; got != want {
		t.Fatalf("GetCPUInfos() returned %d CPUs, want %d", got, want)
	}
	if got, want := infos[0].SocketID, 7; got != want {
		t.Fatalf("SocketID = %d, want %d", got, want)
	}
}
