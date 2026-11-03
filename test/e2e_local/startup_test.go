/*
Copyright 2026 The Kubernetes Authors.

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
	"context"
	"errors"
	"fmt"
	"net"
	"os/exec"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

var _ = ginkgo.Describe("[Local] dracpu startup", func() {
	ginkgo.It("should fail before starting the driver when the healthz address is occupied", func() {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
		ginkgo.DeferCleanup(func() {
			gomega.Expect(listener.Close()).To(gomega.Succeed())
		})

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		address := listener.Addr().String()
		cmdline := []string{binPath, "--bind-address=" + address}
		fmt.Fprintf(ginkgo.GinkgoWriter, "running: %v\n", cmdline)

		// #nosec G204 -- the executable is the built test binary and the only argument is a test-owned port.
		out, err := exec.CommandContext(ctx, cmdline[0], cmdline[1:]...).CombinedOutput()
		gomega.Expect(ctx.Err()).ToNot(gomega.HaveOccurred(), "dracpu did not exit before the test timeout")
		gomega.Expect(err).To(gomega.HaveOccurred())

		var exitErr *exec.ExitError
		gomega.Expect(errors.As(err, &exitErr)).To(gomega.BeTrue(), "dracpu did not exit with an ExitError")
		gomega.Expect(exitErr.ExitCode()).ToNot(gomega.Equal(0))
		gomega.Expect(string(out)).To(gomega.ContainSubstring("failed to bind HTTP server to " + address))
		gomega.Expect(string(out)).ToNot(gomega.ContainSubstring("driver started"))
	})
})
