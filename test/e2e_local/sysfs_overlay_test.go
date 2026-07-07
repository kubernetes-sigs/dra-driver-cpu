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

package e2e_local

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kubernetes-sigs/dra-driver-cpu/internal/gatherinfo"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

var _ = ginkgo.Describe("[Local] dracpu-gatherinfo with HOST_ROOT and sysfs overlay", func() {
	ginkgo.It("should read the base topology and apply overlay values", func() {
		hostRoot := ginkgo.GinkgoT().TempDir()
		overlayPath := filepath.Join(ginkgo.GinkgoT().TempDir(), "sysfs-overlay.yaml")

		writeLocalTestFile(filepath.Join(hostRoot, "sys/devices/system/cpu/online"), []byte("0\n"))
		writeLocalTestFile(filepath.Join(hostRoot, "sys/devices/system/cpu/smt/control"), []byte("off\n"))
		writeLocalTestFile(filepath.Join(hostRoot, "sys/devices/system/node/node0/cpulist"), []byte("0\n"))
		writeLocalTestFile(filepath.Join(hostRoot, "sys/devices/system/cpu/cpu0/topology/physical_package_id"), []byte("0\n"))
		writeLocalTestFile(filepath.Join(hostRoot, "sys/devices/system/cpu/cpu0/topology/core_id"), []byte("0\n"))
		writeLocalTestFile(filepath.Join(hostRoot, "sys/devices/system/cpu/cpu0/topology/cluster_id"), []byte("0\n"))
		writeLocalTestFile(filepath.Join(hostRoot, "sys/devices/system/cpu/cpu0/cache/index3/level"), []byte("3\n"))
		writeLocalTestFile(filepath.Join(hostRoot, "sys/devices/system/cpu/cpu0/cache/index3/id"), []byte("0\n"))
		writeLocalTestFile(filepath.Join(hostRoot, "sys/devices/system/cpu/cpu0/cache/index3/shared_cpu_list"), []byte("0\n"))
		gomega.Expect(os.MkdirAll(filepath.Join(hostRoot, "sys/devices/system/cpu/cpu0/node0"), 0o755)).To(gomega.Succeed())

		writeLocalTestFile(overlayPath, []byte(`
/sys/devices/system/cpu/smt/control: "on\n"
/sys/devices/system/cpu/cpu0/topology/physical_package_id: "7\n"
`))

		writeLocalTestFile(filepath.Join(hostRoot, "proc/42/comm"), []byte("dracpu\n"))
		writeLocalTestFile(filepath.Join(hostRoot, "proc/42/cmdline"), []byte(
			"/dracpu\x00--cpu-device-mode=grouped\x00--group-by=numanode\x00--sysfs-overlay="+overlayPath+"\x00",
		))
		gomega.Expect(os.Symlink("/", filepath.Join(hostRoot, "proc/42/root"))).To(gomega.Succeed())

		gatherInfoPath := filepath.Join(ginkgo.GinkgoT().TempDir(), "dracpu-gatherinfo")
		gomega.Expect(os.Symlink(binPath, gatherInfoPath)).To(gomega.Succeed())

		cmdline := []string{gatherInfoPath, "--stdout"}
		fmt.Fprintf(ginkgo.GinkgoWriter, "running: %v with HOST_ROOT=%s\n", cmdline, hostRoot)
		cmd := exec.Command(cmdline[0], cmdline[1:]...) //nolint:gosec // Test executes the repository binary through a controlled symlink.
		cmd.Env = replaceEnv(os.Environ(), "HOST_ROOT", hostRoot)
		cmd.Stderr = ginkgo.GinkgoWriter

		out, err := cmd.Output()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		var report gatherinfo.Report
		gomega.Expect(yaml.Unmarshal(out, &report)).To(gomega.Succeed())
		gomega.Expect(report.DriverConfig.SysFSOverlay).To(gomega.Equal(overlayPath))
		gomega.Expect(report.CPUDetails.Topology.NumCPUs).To(gomega.Equal(1))
		gomega.Expect(report.CPUDetails.Topology.NumNUMANodes).To(gomega.Equal(1))
		gomega.Expect(report.CPUDetails.Topology.SMTEnabled).To(gomega.BeTrue())
		gomega.Expect(report.CPUDetails.CPUs).To(gomega.HaveLen(1))

		cpu := report.CPUDetails.CPUs[0]
		gomega.Expect(cpu.CPUID).To(gomega.Equal(0))
		gomega.Expect(cpu.SocketID).To(gomega.Equal(7))
		gomega.Expect(cpu.CoreID).To(gomega.Equal(0))
		gomega.Expect(cpu.NUMANodeID).To(gomega.Equal(0))
		gomega.Expect(cpu.UncoreCacheID).To(gomega.Equal(0))
	})
})

func writeLocalTestFile(name string, data []byte) {
	ginkgo.GinkgoHelper()
	gomega.Expect(os.MkdirAll(filepath.Dir(name), 0o755)).To(gomega.Succeed())
	gomega.Expect(os.WriteFile(name, data, 0o600)).To(gomega.Succeed())
}

func replaceEnv(environ []string, key, value string) []string {
	prefix := key + "="
	out := make([]string, 0, len(environ)+1)
	for _, entry := range environ {
		if !strings.HasPrefix(entry, prefix) {
			out = append(out, entry)
		}
	}
	return append(out, prefix+value)
}
