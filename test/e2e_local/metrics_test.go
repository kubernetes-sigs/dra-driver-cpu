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

var _ = ginkgo.Describe("[Local] dracpu --show-metrics", func() {
	ginkgo.It("should output valid JSON with custom metric descriptors", func() {
		cmdline := []string{binPath, "--show-metrics"}
		fmt.Fprintf(ginkgo.GinkgoWriter, "running: %v\n", cmdline)

		cmd := exec.Command(cmdline[0], cmdline[1:]...)
		cmd.Stderr = ginkgo.GinkgoWriter

		out, err := cmd.Output()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())

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

func findDescriptorByName(descriptors []metrics.Descriptor, name string) *metrics.Descriptor {
	for idx := range len(descriptors) {
		desc := &descriptors[idx]
		if desc.Name == name {
			return desc
		}
	}
	return nil
}
