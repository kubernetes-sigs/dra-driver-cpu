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
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/metrics"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("[Local] dracpu introspect metrics", func() {
	ginkgo.It("should output valid JSON with custom metric descriptors", func() {
		out := runCommand(binPath, "introspect", "metrics")

		// TODO: undecided: this import allows us clean validation, but
		// we have now a build dep and the tests are no longer truly blackbox.
		// at this point in time this seems a fair compromise.
		var descriptors []metrics.Descriptor
		gomega.Expect(json.Unmarshal(out, &descriptors)).To(gomega.Succeed())
		gomega.Expect(descriptors).ToNot(gomega.BeEmpty())

		text := strings.TrimSpace(string(out))
		fmt.Fprintf(ginkgo.GinkgoWriter, "output:\n%s\n", text)
		// sanity check on raw output
		gomega.Expect(text).ToNot(gomega.ContainSubstring("go_gc_duration_seconds"))

		gomega.Expect(findDescriptorByName(descriptors, "dra_cpu_allocated_cpus")).ToNot(gomega.BeNil())
	})
})

var _ = ginkgo.Describe("[Local] dracpu root usage", func() {
	ginkgo.It("should list subcommands and compatibility paths", func() {
		cmdline := []string{binPath, "--help"}
		fmt.Fprintf(ginkgo.GinkgoWriter, "running: %v\n", cmdline)

		// #nosec G204 -- the command and arguments are fixed above.
		out, err := exec.Command(cmdline[0], cmdline[1:]...).CombinedOutput()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

		usage := string(out)
		gomega.Expect(usage).To(gomega.ContainSubstring("dracpu gatherinfo [flags]"))
		gomega.Expect(usage).To(gomega.ContainSubstring("dracpu introspect [metrics|config]"))
		gomega.Expect(usage).To(gomega.ContainSubstring("dracpu-gatherinfo [flags]"))
		gomega.Expect(usage).ToNot(gomega.ContainSubstring("--show-metrics"))
	})
})

var _ = ginkgo.DescribeTable("[Local] dracpu command flag isolation",
	func(args []string, expectedError string) {
		cmdline := append([]string{binPath}, args...)
		fmt.Fprintf(ginkgo.GinkgoWriter, "running: %v\n", cmdline)

		// #nosec G204 -- the command and arguments come from the fixed test table below.
		out, err := exec.Command(cmdline[0], cmdline[1:]...).CombinedOutput()
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(string(out)).To(gomega.ContainSubstring(expectedError))
	},
	ginkgo.Entry("rejects root flags before a subcommand",
		[]string{"--config=/does/not/exist", "gatherinfo"},
		"root flags cannot be combined with subcommands"),
	ginkgo.Entry("rejects root flags after a subcommand",
		[]string{"gatherinfo", "--config=/does/not/exist"},
		"flag provided but not defined: -config"),
	ginkgo.Entry("rejects the removed show-metrics flag",
		[]string{"--show-metrics"},
		"flag provided but not defined: -show-metrics"),
)

func findDescriptorByName(descriptors []metrics.Descriptor, name string) *metrics.Descriptor {
	for idx := range len(descriptors) {
		desc := &descriptors[idx]
		if desc.Name == name {
			return desc
		}
	}
	return nil
}
