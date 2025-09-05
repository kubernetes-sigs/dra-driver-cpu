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

package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/discovery"
)

func main() {
	cpuInfoProvider := cpuinfo.NewSystemCPUInfo()
	cpus, err := cpuInfoProvider.GetCPUInfos()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error getting cpu info: %v\n", err)
		os.Exit(1)
	}
	cpuInfo := discovery.DRACPUInfo{
		Buildinfo: discovery.NewBuildinfo(),
		CPUs:      cpus,
	}
	err = json.NewEncoder(os.Stdout).Encode(cpuInfo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error encoding cpu info: %v\n", err)
		os.Exit(2)
	}
}
