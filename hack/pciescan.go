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

package main

import (
	"errors"
	"flag"
	"fmt"
	"go/format"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"text/template"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/device"
	"k8s.io/klog/v2/textlogger"
)

const (
	scanCommand = "scan-pci"
	dumpCommand = "dump-mapfs"
)

func main() {
	sysfsRoot := device.SysfsRoot

	fs := flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	fs.StringVar(&sysfsRoot, "sysfs-root", sysfsRoot, "sysfs root path")
	config := textlogger.NewConfig()
	config.AddFlags(fs)
	if err := fs.Parse(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	logger := textlogger.NewLogger(config)

	sysfs := os.DirFS(sysfsRoot).(device.SysFS)

	var err error
	args := fs.Args()
	if len(args) == 0 {
		err = runScan(logger, sysfs)
	} else {
		switch args[0] {
		case scanCommand:
			err = runScan(logger, sysfs)
		case dumpCommand:
			err = runDumpMapFS(logger, sysfs, args[1:])
		default:
			err = fmt.Errorf("unknown subcommand: %s", args[0])
		}
	}
	if err != nil {
		logger.Error(err, "failed to run")
		os.Exit(1)
	}
}

func runScan(logger logr.Logger, sysfs device.SysFS) error {
	domains, err := device.PCIeDomainsFromFS(logger, sysfs)
	if err != nil {
		return fmt.Errorf("failed to scan the PCIe domains: %w", err)
	}

	logger.V(2).Info("found PCIe domains", "count", len(domains))
	for _, dom := range domains {
		logger.Info("PCIe domain", "name", dom.String())
	}

	onlineCPUs, err := device.OnlineCPUs(logger, sysfs)
	if err != nil {
		return fmt.Errorf("failed to get the online CPUs: %w", err)
	}

	orphans := device.FindOrphanedCPUs(domains, onlineCPUs)
	logger.V(2).Info("found orphaned CPUs", "count", orphans.Size())
	return nil
}

type mapFSEntry struct {
	Path   string
	Data   []byte
	IsLink bool
}

func (fse mapFSEntry) Emit(w io.Writer) {
	fmt.Fprintf(w, "%q: &fstest.MapFile{\n", fse.Path)
	fmt.Fprintf(w, "\tData: []byte(%q),\n", string(fse.Data))
	if fse.IsLink {
		fmt.Fprintln(w, "\tMode: fs.ModeSymlink,")
	}
	fmt.Fprintln(w, "},")
}

type mapFSGen struct {
	Func     string
	Pkg      string
	BuildTag string
	entries  []mapFSEntry
}

func (gen *mapFSGen) SetDefaults() {
	gen.Func = "makeSysfsFixture"
	gen.Pkg = "device"
}

func (gen *mapFSGen) Reset() {
	gen.entries = nil
}

func runDumpMapFS(logger logr.Logger, sysfs device.SysFS, args []string) error {
	gen := &mapFSGen{}
	gen.SetDefaults()
	fs := flag.NewFlagSet(dumpCommand, flag.ContinueOnError)
	fs.StringVar(&gen.Func, "func", gen.Func, "function name for the generated MapFS fixture")
	fs.StringVar(&gen.Pkg, "pkg", gen.Pkg, "package name for the generated MapFS fixture")
	addBuildTag := fs.Bool("add-build-tag", false, "inject a //go:build constraint for the current architecture ("+runtime.GOARCH+")")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		return err
	}

	if *addBuildTag {
		gen.BuildTag = runtime.GOARCH
	}

	gen.Reset()
	err := gen.CollectFromSysfs(logger, sysfs)
	if err != nil {
		return fmt.Errorf("failed to collect sysfs snapshot: %w", err)
	}
	src, err := gen.MapFSSource()
	if err != nil {
		return fmt.Errorf("failed to generate MapFS source: %w", err)
	}
	fmt.Print(string(src))
	return nil
}

func (gen *mapFSGen) ReadDataFrom(sysfs device.SysFS, sysPath string) error {
	data, err := fs.ReadFile(sysfs, sysPath)
	if err != nil {
		return fmt.Errorf("reading data %q: %w", sysPath, err)
	}
	gen.entries = append(gen.entries, mapFSEntry{
		Path: sysPath,
		Data: data,
	})
	return nil
}

func (gen *mapFSGen) ReadLinkFrom(sysfs device.SysFS, sysPath string) (string, error) {
	target, err := fs.ReadLink(sysfs, sysPath)
	if err != nil {
		return "", fmt.Errorf("reading link %q: %w", sysPath, err)
	}
	gen.entries = append(gen.entries, mapFSEntry{
		Path:   sysPath,
		Data:   []byte(target),
		IsLink: true,
	})
	return target, nil
}

func (gen *mapFSGen) Emit(w io.Writer) {
	fmt.Fprintf(w, "return fstest.MapFS{\n")
	for _, entry := range gen.entries {
		entry.Emit(w)
	}
	fmt.Fprintf(w, "}\n")
}

func (gen *mapFSGen) CollectFromSysfs(logger logr.Logger, sysfs device.SysFS) error {
	err := gen.ReadDataFrom(sysfs, filepath.Join("devices", "system", "cpu", "online"))
	if err != nil {
		return err
	}
	err = device.ScanPCIeDevices(logger, sysfs, func(pciDev device.PCIeDevice) error {
		// entry point link in bus/pci/devices
		target, err := gen.ReadLinkFrom(sysfs, pciDev.SysfsPath())
		if err != nil {
			return err
		}
		// resolve the link and fetch the real content to sit in devices/pciDOMAIN/...
		devicePath := filepath.Clean(filepath.Join(pciDev.BaseDirPath(), target))
		err = gen.ReadDataFrom(sysfs, filepath.Join(devicePath, "class"))
		if err != nil {
			return err
		}
		err = gen.ReadDataFrom(sysfs, filepath.Join(devicePath, "local_cpulist"))
		if err != nil {
			return err
		}
		err = gen.ReadDataFrom(sysfs, filepath.Join(devicePath, "numa_node"))
		if err != nil {
			return err
		}
		return nil
	})
	sort.SliceStable(gen.entries, func(i, j int) bool {
		return gen.entries[i].Path < gen.entries[j].Path
	})
	return err
}

func (gen *mapFSGen) MapFSSource() ([]byte, error) {
	preambleText := `
{{- if .BuildTag}}
//go:build {{.BuildTag}}

{{end -}}
// autogenerated - DO NOT EDIT!
// regenerate from the project root with: go run hack/pciescan.go dump-mapfs --func {{.Func}} --pkg {{.Pkg}}{{if .BuildTag}} --add-build-tag{{end}}
package {{.Pkg}}

import (
    "io/fs"
    "testing/fstest"
)

func {{.Func}}() fstest.MapFS {
`
	var sb strings.Builder
	preamble, err := template.New("preamble").Parse(preambleText)
	if err != nil {
		return nil, err
	}
	if err := preamble.Execute(&sb, gen); err != nil {
		return nil, err
	}
	gen.Emit(&sb)
	fmt.Fprintf(&sb, "}\n") // func end
	return format.Source([]byte(sb.String()))
}
