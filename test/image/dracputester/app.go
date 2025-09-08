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
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/discovery"
	"golang.org/x/sys/unix"
	"k8s.io/utils/cpuset"
)

const (
	cgroupPath = "fs/cgroup"
	cpusetFile = "cpuset.cpus.effective"
)

func cpuSetPath(sysRoot string) string {
	return filepath.Join(sysRoot, cgroupPath, cpusetFile)
}

func cpuSet(sysRoot string) (cpuset.CPUSet, error) {
	data, err := os.ReadFile(cpuSetPath(sysRoot))
	if err != nil {
		return cpuset.New(), err
	}
	return cpuset.Parse(strings.TrimSpace(string(data)))
}

func main() {
	cpus, err := cpuSet("/sys")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error determining allocated cpus: %v\n", err)
		os.Exit(1)
	}
	info := discovery.DRACPUTester{
		Buildinfo: discovery.NewBuildinfo(),
		Allocation: discovery.DRACPUAllocation{
			CPUs: cpus.String(),
		},
	}
	err = json.NewEncoder(os.Stdout).Encode(info)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error encoding info: %v\n", err)
		os.Exit(2)
	}

	signalCh := make(chan os.Signal, 2)
	defer func() {
		close(signalCh)
	}()
	signal.Notify(signalCh, os.Interrupt, unix.SIGINT)
	<-signalCh
	fmt.Fprintf(os.Stderr, "exiting: received signal\n")
}
