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

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/kubernetes-sigs/dra-driver-cpu/internal/app"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/ctxlog"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/driverconfig"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/subcommands"
	"golang.org/x/sys/unix"
)

const (
	configFlagHelp = "Path to a YAML driver configuration file. Configuration values are applied in order: " +
		"built-in defaults, then file values, then explicit CLI flags. " +
		"Only values explicitly set on the command line override earlier layers. " +
		"If empty, only CLI flags and built-in defaults are used."
)

var (
	driverFlags driverconfig.Config
	configFile  string
)

func init() {
	// --config is kept outside Config to avoid a self-referential loop,
	// following the convention used by kubeadm and similar tools.
	flag.StringVar(&configFile, "config", "", configFlagHelp)
	driverFlags.AddFlags(flag.CommandLine)
	flag.CommandLine.Usage = func() {
		printRootUsage(flag.CommandLine)
	}
}

func printRootUsage(fs *flag.FlagSet) {
	commandName := filepath.Base(fs.Name())
	if commandName == "." || commandName == string(filepath.Separator) {
		commandName = "dracpu"
	}

	fmt.Fprintf(fs.Output(), "Usage:\n  %s [flags]\n  %s gatherinfo [flags]\n  %s introspect metrics\n\nCompatibility paths:\n  dracpu-gatherinfo [flags]\n\nFlags:\n", commandName, commandName, commandName)
	fs.PrintDefaults()
}

func main() {
	if filepath.Base(os.Args[0]) == "dracpu-gatherinfo" {
		os.Args = append([]string{"dracpu", "gatherinfo"}, os.Args[1:]...)
	}

	ctxlog.AddFlags(flag.CommandLine)
	flag.Parse()
	logger := ctxlog.Setup()

	if len(flag.Args()) > 0 {
		if flag.NFlag() > 0 {
			fmt.Fprintln(os.Stderr, "dracpu: root flags cannot be combined with subcommands")
			os.Exit(1)
		}
		opts := subcommands.Options{
			Logger: logger,
			Stdout: os.Stdout,
			Stderr: os.Stderr,
		}

		if err := subcommands.Run(flag.Args(), opts); err != nil {
			fmt.Fprintf(os.Stderr, "dracpu: %v\n", err)
			os.Exit(1)
		}
		return
	}

	cfg, err := driverconfig.Resolve(logger, []driverconfig.Source{
		// the ordering of Sources matters and determines the
		// precedence of layers (aka overrides).
		// Per our current policy, the priority (rightmost wins)
		// is defaults -> config -> flags.
		driverconfig.FromFile(configFile),
		driverconfig.FromFlags(flag.CommandLine),
	})
	if err != nil {
		logger.Error(err, "failed to load configuration")
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, unix.SIGINT, unix.SIGTERM)
	go func() {
		select {
		case sig := <-sigCh:
			signal.Stop(sigCh)
			logger.Info("exiting", "reason", "received signal", "signal", sig.String())
			cancel()
		case <-ctx.Done():
		}
	}()

	err = app.Run(ctx, logger, cfg, app.DefaultDependencies())
	signal.Stop(sigCh)
	cancel()
	if err != nil {
		logger.Error(err, "failed to run")
		os.Exit(1)
	}
}
