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

	"github.com/kubernetes-sigs/dra-driver-cpu/internal/driverconfig"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"sigs.k8s.io/yaml"
)

var _ = ginkgo.Describe("[Local] dracpu introspect config", func() {
	ginkgo.It("should output defaults matching compiled-in defaults", func() {
		out := runCommand(binPath, "introspect", "config")

		expected := driverconfig.Default()
		got := driverconfig.Config{}
		gomega.Expect(yaml.Unmarshal(out, &got)).To(gomega.Succeed())
		gomega.Expect(got).To(gomega.Equal(expected))
	})

	ginkgo.It("should merge file values with defaults", func() {
		tmpDir := ginkgo.GinkgoT().TempDir()
		cfgPath := filepath.Join(tmpDir, "config.yaml")

		expected := driverconfig.Default()
		expected.ReservedCPUs = "0-3"
		expected.CPUDeviceMode = "individual"

		cfgContent := []byte(`
reservedCPUs: "0-3"
cpuDeviceMode: individual
`)
		gomega.Expect(os.WriteFile(cfgPath, cfgContent, 0600)).To(gomega.Succeed())

		out := runCommand(binPath, "introspect", "config", cfgPath)

		var got driverconfig.Config
		gomega.Expect(yaml.Unmarshal(out, &got)).To(gomega.Succeed())
		gomega.Expect(got).To(gomega.Equal(expected))
	})

	ginkgo.It("should fail when config file does not exist", func() {
		cmdline := []string{binPath, "introspect", "config", "/does/not/exist.yaml"}
		fmt.Fprintf(ginkgo.GinkgoWriter, "running: %v\n", cmdline)

		// #nosec G204 -- the command and arguments are fixed above.
		out, err := exec.Command(cmdline[0], cmdline[1:]...).CombinedOutput()
		gomega.Expect(err).To(gomega.HaveOccurred())
		fmt.Fprintf(ginkgo.GinkgoWriter, "output:\n%s\n", string(out))
	})

	ginkgo.It("should succeed with --help", func() {
		cmdline := []string{binPath, "introspect", "config", "--help"}
		fmt.Fprintf(ginkgo.GinkgoWriter, "running: %v\n", cmdline)

		cmd := exec.Command(cmdline[0], cmdline[1:]...)
		cmd.Stderr = ginkgo.GinkgoWriter

		err := cmd.Run()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	})

	ginkgo.It("should reject extra positional arguments", func() {
		cmdline := []string{binPath, "introspect", "config", "a", "b"}
		fmt.Fprintf(ginkgo.GinkgoWriter, "running: %v\n", cmdline)

		// #nosec G204 -- the command and arguments are fixed above.
		out, err := exec.Command(cmdline[0], cmdline[1:]...).CombinedOutput()
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(string(out)).To(gomega.ContainSubstring("config accepts up to 1 positional arguments"))
	})
})
