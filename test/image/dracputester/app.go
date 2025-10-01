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
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"time"

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

func getAffinity() (cpuset.CPUSet, error) {
	var unixCS unix.CPUSet
	err := unix.SchedGetaffinity(os.Getpid(), &unixCS)
	if err != nil {
		return cpuset.New(), err
	}

	var allowedCPUs []int
	for i := 0; i < runtime.NumCPU(); i++ {
		if unixCS.IsSet(i) {
			allowedCPUs = append(allowedCPUs, i)
		}
	}
	return cpuset.New(allowedCPUs...), nil
}

func main() {
	runTimeout := 0 * time.Second
	flag.DurationVar(&runTimeout, "run-for", runTimeout, "run for the given duration before exit after logging. Use 0 to run forever")
	flag.Parse()

	cpus, err := cpuSet("/sys")
	if err != nil {
		fmt.Fprintf(os.Stderr, "error determining allocated cpus: %v\n", err)
		os.Exit(1)
	}
	cpuAff, err := getAffinity()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error determining CPU affinity: %v\n", err)
		os.Exit(2)
	}
	info := discovery.DRACPUTester{
		Buildinfo: discovery.NewBuildinfo(),
		Allocation: discovery.DRACPUAllocation{
			CPUs: cpus.String(),
		},
		Runtimeinfo: discovery.DRACPURuntimeinfo{
			CPUAffinity: cpuAff.String(),
		},
	}
	err = json.NewEncoder(os.Stdout).Encode(info)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error encoding info: %v\n", err)
		os.Exit(2)
	}

	if runTimeout > 0 {
		time.Sleep(runTimeout)
	} else {
		signalCh := make(chan os.Signal, 2)
		defer func() {
			close(signalCh)
		}()
		signal.Notify(signalCh, os.Interrupt, unix.SIGINT)
		<-signalCh
		fmt.Fprintf(os.Stderr, "exiting: received signal\n")
	}
}
