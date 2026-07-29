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

package subcommands

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/driverconfig"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/gatherinfo"
	cpumetrics "github.com/kubernetes-sigs/dra-driver-cpu/pkg/metrics"
)

// Options configures subcommand execution.
type Options struct {
	DriverConfig driverconfig.Config
	Logger       logr.Logger
	Stdout       io.Writer
	Stderr       io.Writer
}

// Run dispatches a dracpu subcommand.
func Run(args []string, opts Options) error {
	if len(args) == 0 {
		return nil
	}

	switch args[0] {
	case "gatherinfo":
		return gatherinfo.Run(args[1:], gatherinfo.Options{
			DriverConfig: opts.DriverConfig,
		}, opts.Logger)
	case "introspect":
		return runIntrospect(args[1:], opts.Stdout, opts.Stderr)
	default:
		return fmt.Errorf("unknown subcommand %q; supported subcommands: gatherinfo, introspect", args[0])
	}
}

func runIntrospect(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("dracpu introspect", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), `Usage: %s <subcommand>

Available subcommands:
metrics\tPrint JSON metadata for custom dra_cpu_* metrics.
`, fs.Name())
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() == 0 {
		return fmt.Errorf("introspect requires a subcommand; supported subcommands: metrics")
	}

	switch fs.Arg(0) {
	case "metrics":
		return runMetrics(fs.Args()[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown introspect subcommand %q; supported subcommands: metrics", fs.Arg(0))
	}
}

func runMetrics(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("dracpu introspect metrics", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: %s\n", fs.Name())
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("metrics does not accept positional arguments: %s", strings.Join(fs.Args(), " "))
	}

	return cpumetrics.WriteJSON(stdout)
}
