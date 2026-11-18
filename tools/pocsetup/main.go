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
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/go-logr/stdr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/setup"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: pocsetup {runtime-config|check-runtime|preflight|hugepages|check-cri}")
	}
	logger := stdr.New(log.New(os.Stderr, "", 0))
	switch args[0] {
	case "runtime-config":
		return setup.ConfigStream(os.Stdin, os.Stdout)
	case "check-runtime":
		return setup.CheckRuntime(os.Stdin)
	case "check-cri":
		return setup.CheckCRI(os.Stdin)
	case "preflight":
		fs := flag.NewFlagSet("preflight", flag.ContinueOnError)
		root := fs.String("root", "/", "Root containing proc and sys for read-only checks")
		memory := fs.Bool("memory", true, "Require both hugepage sizes provisioned before creating nodes")
		node := fs.Bool("kind-node", false, "Check kind-node ancestor hugetlb limits")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		return setup.CheckEnvironment(logger, *root, *memory, *node)
	case "hugepages":
		fs := flag.NewFlagSet("hugepages", flag.ContinueOnError)
		root := fs.String("sysfs-root", "/", "Root containing sysfs")
		config := fs.String("config", "", "HugePageProvision YAML file (writes host page reservations)")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if *config == "" {
			return fmt.Errorf("specify --config=hack/poc/hugepages.yaml; this changes host-wide page reservations")
		}
		provision, err := setup.ReadConfiguration(*config)
		if err != nil {
			return fmt.Errorf("read hugepage provisioning file: %w", err)
		}
		return setup.ProvisionHugepages(logger, provision, *root)
	default:
		return fmt.Errorf("unknown setup command %q; use runtime-config, check-runtime, preflight, hugepages, or check-cri", args[0])
	}
}
