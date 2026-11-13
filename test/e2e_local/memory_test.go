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

package e2e_local

import (
	"os/exec"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("[Local] memory family switch", func() {
	ginkgo.DescribeTable("accepts explicit booleans", func(value string) {
		// #nosec G204 -- values are the fixed boolean table entries below.
		out, err := exec.Command(binPath, "--enable-memory="+value, "--help").CombinedOutput()
		gomega.Expect(err).NotTo(gomega.HaveOccurred(), "%s", out)
		gomega.Expect(string(out)).To(gomega.ContainSubstring("-enable-memory"))
	}, ginkgo.Entry("enabled", "true"), ginkgo.Entry("disabled", "false"))

	ginkgo.It("rejects a non-boolean value", func() {
		out, err := exec.Command(binPath, "--enable-memory=perhaps", "--help").CombinedOutput()
		gomega.Expect(err).To(gomega.HaveOccurred())
		gomega.Expect(string(out)).To(gomega.ContainSubstring("invalid boolean value"))
	})
})
