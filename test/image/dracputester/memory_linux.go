//go:build linux

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
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/discovery"
	"golang.org/x/sys/unix"
	"k8s.io/utils/cpuset"
)

func memoryReport(sfs fs.FS) (discovery.MemoryProbe, error) {
	r := discovery.MemoryProbe{}
	for _, field := range []struct {
		file string
		dst  *string
	}{
		{"cpuset.cpus.effective", &r.CPUs}, {"cpuset.mems.effective", &r.Mems}, {"memory.max", &r.MemoryMax},
		{"hugetlb.2MB.max", &r.Huge2MMax}, {"hugetlb.1GB.max", &r.Huge1GMax},
	} {
		data, err := fs.ReadFile(sfs, field.file)
		if os.IsNotExist(err) && field.file == "hugetlb.1GB.max" {
			continue
		}
		if err != nil {
			return r, fmt.Errorf("read cgroup %s; enable the required cgroup v2 controller: %w", field.file, err)
		}
		*field.dst = strings.TrimSpace(string(data))
	}
	return r, nil
}

func runMemoryPOC() error {
	report, err := memoryReport(os.DirFS("/sys/fs/cgroup"))
	if err != nil {
		return err
	}
	for _, field := range []struct{ env, actual string }{
		{"POC_MEMS", report.Mems}, {"POC_MEMORY_BYTES", report.MemoryMax}, {"POC_HUGE_2M_BYTES", report.Huge2MMax}, {"POC_HUGE_1G_BYTES", report.Huge1GMax},
	} {
		if expected := os.Getenv(field.env); expected != "" && field.actual != expected {
			return fmt.Errorf("%s: got %s, want %s", field.env, field.actual, expected)
		}
	}
	if text := os.Getenv("POC_CPU_COUNT"); text != "" {
		want, err := strconv.Atoi(text)
		if err != nil {
			return fmt.Errorf("parse POC_CPU_COUNT: %w", err)
		}
		actual, err := cpuset.Parse(report.CPUs)
		if err != nil {
			return err
		}
		if actual.Size() != want {
			return fmt.Errorf("CPU count %d, want %d", actual.Size(), want)
		}
		affinity, err := getAffinity(logr.Discard())
		if err != nil {
			return err
		}
		if !affinity.Equals(actual) {
			return fmt.Errorf("CPU affinity %s differs from effective cpuset %s", affinity.String(), actual.String())
		}
	}
	if os.Getenv("POC_MEMORY_BYTES") != "" {
		data := make([]byte, 1<<20)
		for i := 0; i < len(data); i += 4096 {
			data[i] = 1
		}
		// Observe each write so the compiler cannot turn this into a no-op probe.
		for i := 0; i < len(data); i += 4096 {
			if data[i] != 1 {
				return fmt.Errorf("regular memory touch failed")
			}
		}
		report.Touched = append(report.Touched, "memory")
	}
	for _, page := range []struct {
		env, kind  string
		size, flag int
	}{
		{"POC_HUGE_2M_BYTES", "hugepages-2m", 2 << 20, unix.MAP_HUGE_2MB},
		{"POC_HUGE_1G_BYTES", "hugepages-1g", 1 << 30, unix.MAP_HUGE_1GB},
	} {
		value := os.Getenv(page.env)
		if value == "" || value == "0" {
			continue
		}
		data, err := unix.Mmap(-1, 0, page.size, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_PRIVATE|unix.MAP_ANONYMOUS|unix.MAP_HUGETLB|page.flag)
		if err != nil {
			return fmt.Errorf("allocate %s; provision pages on the selected NUMA node and permit the workload's hugetlb group: %w", page.kind, err)
		}
		for i := 0; i < len(data); i += 4096 {
			data[i] = 1
		}
		for i := 0; i < len(data); i += 4096 {
			if data[i] != 1 {
				return fmt.Errorf("%s memory touch failed", page.kind)
			}
		}
		if err := unix.Munmap(data); err != nil {
			return fmt.Errorf("unmap %s: %w", page.kind, err)
		}
		report.Touched = append(report.Touched, page.kind)
	}
	return json.NewEncoder(os.Stdout).Encode(report)
}
