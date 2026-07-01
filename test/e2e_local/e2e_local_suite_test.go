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
	"path/filepath"
	"runtime"
	"testing"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var binPath string

func TestE2ELocal(t *testing.T) {
	gomega.RegisterFailHandler(ginkgo.Fail)
	ginkgo.RunSpecs(t, "DRA CPU Driver E2E Local Suite")
}

var _ = ginkgo.BeforeSuite(func() {
	_, file, _, ok := runtime.Caller(0)
	gomega.Expect(ok).To(gomega.BeTrue(), "cannot retrieve test file path")

	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	binPath = filepath.Join(repoRoot, "bin", "dracpu")
	fmt.Fprintf(ginkgo.GinkgoWriter, "using binary at %q\n", binPath)

	_, err := os.Stat(binPath)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(),
		"binary not found at %q; run 'make build-dracpu' first", binPath)
})
