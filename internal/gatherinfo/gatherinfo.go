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

package gatherinfo

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/buildinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/driverconfig"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/sysfs"
	"sigs.k8s.io/yaml"
)

const LayoutVersion = "v1"
const driverProcessName = "dracpu"

type Report struct {
	ToolVersion   ToolVersion         `json:"toolVersion"`
	LayoutVersion string              `json:"layoutVersion"`
	CPUDetails    CPUDetails          `json:"cpuDetails"`
	DriverConfig  driverconfig.Config `json:"driverConfig"`
}

type CPUDetails struct {
	Topology TopologySummary `json:"topology"`
	CPUs     []CPU           `json:"cpus"`
}

type TopologySummary struct {
	NumCPUs        int  `json:"numCPUs"`
	NumCores       int  `json:"numCores"`
	NumUncoreCache int  `json:"numUncoreCache"`
	NumSockets     int  `json:"numSockets"`
	NumNUMANodes   int  `json:"numNUMANodes"`
	SMTEnabled     bool `json:"smtEnabled"`
}

type CPU struct {
	CPUID          int    `json:"cpuID"`
	CoreID         int    `json:"coreID"`
	SocketID       int    `json:"socketID"`
	ClusterID      int    `json:"clusterID"`
	NUMANodeID     int    `json:"numaNodeID"`
	NUMANodeCPUSet string `json:"numaNodeCPUSet,omitempty"`
	Sibling        int    `json:"sibling"`
	CoreType       string `json:"coreType,omitempty"`
	UncoreCacheID  int    `json:"uncoreCacheID"`
}

type ToolVersion struct {
	GoVersion   string `json:"goVersion,omitempty"`
	VCSRevision string `json:"vcsRevision,omitempty"`
	VCSTime     string `json:"vcsTime,omitempty"`
}

type Options struct {
	OutputParentDir   string
	DriverCmdlinePath string
}

func Run(args []string, opts Options, logger logr.Logger) error {
	fs := flag.NewFlagSet("dracpu gatherinfo", flag.ContinueOnError)
	outputDir := fs.String("output-dir", opts.OutputParentDir, "Parent directory for debug artifacts (default: temporary directory)")
	emitStdout := fs.Bool("stdout", false, "Write the YAML report to stdout instead of a file")

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *emitStdout && *outputDir != "" {
		return fmt.Errorf("--stdout and --output-dir cannot be used together")
	}

	driverCmdlinePath, err := opts.driverCmdlinePath()
	if err != nil {
		return err
	}

	report, err := collectReport(logger, driverCmdlinePath)
	if err != nil {
		return err
	}

	if *emitStdout {
		return writeYAML(os.Stdout, report)
	}

	outputFile, outputPath, err := createOutputFile(*outputDir, time.Now(), os.Getpid())
	if err != nil {
		return err
	}
	if err := writeReportFile(outputFile, outputPath, report); err != nil {
		return err
	}

	logger.Info("debug info collected", "path", outputPath)
	return nil
}

func (o Options) driverCmdlinePath() (string, error) {
	if o.DriverCmdlinePath != "" {
		return o.DriverCmdlinePath, nil
	}
	return defaultDriverCmdlinePath()
}

func defaultDriverCmdlinePath() (string, error) {
	cmdlinePath, err := findDriverCmdlinePath(cpuinfo.ProcfsRoot(), driverProcessName)
	if err != nil {
		return "", fmt.Errorf("failed to find running %q process: %w", driverProcessName, err)
	}
	return cmdlinePath, nil
}

func findDriverCmdlinePath(procRoot, processName string) (string, error) {
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return "", err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}

		processRoot := filepath.Join(procRoot, entry.Name())
		comm, err := os.ReadFile(filepath.Join(processRoot, "comm"))
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(comm)) == processName {
			return filepath.Join(processRoot, "cmdline"), nil
		}
	}

	return "", fmt.Errorf("process %q not found under %s", processName, procRoot)
}

func createOutputFile(parentDir string, now time.Time, pid int) (*os.File, string, error) {
	fileName := fmt.Sprintf("dracpu-gatherinfo-%s-%d.yaml", now.Format("20060102-150405"), pid)
	if parentDir == "" {
		f, err := os.CreateTemp("", "dracpu-gatherinfo-*.yaml")
		if err != nil {
			return nil, "", fmt.Errorf("failed to create temp file: %w", err)
		}
		return f, f.Name(), nil
	}

	parentDir = filepath.Clean(parentDir)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return nil, "", fmt.Errorf("failed to create output dir: %w", err)
	}

	outputPath := filepath.Join(parentDir, fileName)
	f, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, "", fmt.Errorf("output file already exists: %s", outputPath)
		}
		return nil, "", fmt.Errorf("failed to open %s: %w", outputPath, err)
	}
	return f, outputPath, nil
}

func collectReport(logger logr.Logger, driverCmdlinePath string) (Report, error) {
	driverConfig, err := driverconfig.Resolve(logger, []driverconfig.Source{
		ConfigSource{DriverCmdlinePath: driverCmdlinePath},
	})
	if err != nil {
		return Report{}, fmt.Errorf("failed to resolve driver config: %w", err)
	}
	overlayPath := driverConfig.SysFSOverlay
	if overlayPath != "" {
		overlayPath = driverFilesystemPath(driverCmdlinePath, overlayPath)
	}
	sfs, err := sysfs.NewOverlayFromFile(sysfs.Host(), overlayPath)
	if err != nil {
		return Report{}, fmt.Errorf("load configured sysfs overlay %q from driver filesystem: %w", driverConfig.SysFSOverlay, err)
	}
	sys := cpuinfo.NewSystemCPUInfo(sfs)

	topology, err := sys.GetCPUTopology(logger)
	if err != nil {
		return Report{}, fmt.Errorf("failed to get CPU topology: %w", err)
	}

	cpus, err := sys.GetCPUInfos(logger)
	if err != nil {
		return Report{}, fmt.Errorf("failed to get CPU infos: %w", err)
	}

	return Report{
		ToolVersion:   readToolVersion(),
		LayoutVersion: LayoutVersion,
		CPUDetails: CPUDetails{
			Topology: makeTopologySummary(topology),
			CPUs:     makeCPUList(cpus),
		},
		DriverConfig: driverConfig,
	}, nil
}

// driverFilesystemPath resolves a path in the running driver's mount namespace.
// The cmdline file and the root/cwd links are siblings under /proc/<pid>.
func driverFilesystemPath(driverCmdlinePath, name string) string {
	processDir := filepath.Dir(driverCmdlinePath)
	if filepath.IsAbs(name) {
		return filepath.Join(processDir, "root", strings.TrimPrefix(filepath.Clean(name), string(filepath.Separator)))
	}

	// /proc/<pid>/cwd is a symlink, so path traversal must happen after that symlink has been resolved.
	// e.g cwd/../overlay.yaml must refer to the parent of the driver's working directory, not /proc/<pid>/overlay.yaml.
	return filepath.Join(processDir, "cwd") + string(filepath.Separator) + name
}

func makeTopologySummary(topology *cpuinfo.CPUTopology) TopologySummary {
	if topology == nil {
		return TopologySummary{}
	}

	return TopologySummary{
		NumCPUs:        topology.NumCPUs,
		NumCores:       topology.NumCores,
		NumUncoreCache: topology.NumUncoreCache,
		NumSockets:     topology.NumSockets,
		NumNUMANodes:   topology.NumNUMANodes,
		SMTEnabled:     topology.SMTEnabled,
	}
}

func makeCPUList(cpus []cpuinfo.CPUInfo) []CPU {
	out := make([]CPU, 0, len(cpus))
	for _, info := range cpus {
		cpu := CPU{
			CPUID:          info.CpuID,
			CoreID:         info.CoreID,
			SocketID:       info.SocketID,
			ClusterID:      info.ClusterID,
			NUMANodeID:     info.NUMANodeID,
			NUMANodeCPUSet: info.NumaNodeCPUSet.String(),
			Sibling:        info.SiblingCPUID,
			UncoreCacheID:  info.UncoreCacheID,
		}
		if coreType := info.CoreType.String(); coreType != "" {
			cpu.CoreType = coreType
		}
		out = append(out, cpu)
	}
	return out
}

// we need a complex config source because gatherinfo wants to progressively
// reconstruct a driver config from the perspective of an outside process,
// so we need to tolerate errors the normal flow should not.
type ConfigSource struct {
	DriverCmdlinePath string
}

func (ConfigSource) Name() string {
	return "gatherinfo"
}

func (cs ConfigSource) Apply(logger logr.Logger, cfg *driverconfig.Config) error {
	cmdline, err := readCmdlineFile(cs.DriverCmdlinePath)
	if err != nil || len(cmdline) == 0 {
		return nil
	}

	if filepath.Base(cmdline[0]) != "dracpu" {
		return nil
	}

	var conf driverconfig.Config // storage space for flag values
	fs := flag.NewFlagSet("detect-driver-config", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	conf.AddFlags(fs)
	var configFile string
	fs.StringVar(&configFile, "config", "", "")
	if err := fs.Parse(knownConfigArgs(fs, cmdline[1:])); err != nil {
		return nil
	}

	// intentionally ignore errors: gatherinfo tolerates partial config
	if configFile != "" {
		// resolve the path in the container namespace
		configFile = driverFilesystemPath(cs.DriverCmdlinePath, configFile)
		_ = driverconfig.FromFile(configFile).Apply(logger, cfg)
	}

	// flags applied last so they win over file values
	_ = driverconfig.FromFlags(fs).Apply(logger, cfg)
	return nil
}

func readCmdlineFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}

	parts := strings.Split(string(data), "\x00")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts, nil
}

// knownConfigArgs filters /proc/1/cmdline down to flags registered in fs.
func knownConfigArgs(fs *flag.FlagSet, args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			break
		}
		name, hasValue, ok := splitFlag(arg)
		if !ok || fs.Lookup(name) == nil {
			if !hasValue && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
			}
			continue
		}
		out = append(out, arg)
		if !hasValue && i+1 < len(args) {
			out = append(out, args[i+1])
			i++
		}
	}
	return out
}

func splitFlag(arg string) (name string, hasValue bool, ok bool) {
	if !strings.HasPrefix(arg, "-") || arg == "-" {
		return "", false, false
	}
	name = strings.TrimLeft(arg, "-")
	if name == "" {
		return "", false, false
	}
	if key, _, found := strings.Cut(name, "="); found {
		return key, true, true
	}
	return name, false, true
}

func readToolVersion() ToolVersion {
	info := buildinfo.Read()
	return ToolVersion{
		GoVersion:   info.GoVersion,
		VCSRevision: info.VCSRevision,
		VCSTime:     info.VCSTime,
	}
}

func writeReportFile(f *os.File, path string, v any) error {
	err := writeYAML(f, v)
	if closeErr := f.Close(); err == nil && closeErr != nil {
		err = fmt.Errorf("failed to close %s: %w", path, closeErr)
	}
	if err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return errors.Join(err, fmt.Errorf("failed to clean up %s: %w", path, removeErr))
		}
		return err
	}
	return nil
}

func writeYAML(w io.Writer, v any) error {
	data, err := marshalYAML(v)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("failed to write YAML: %w", err)
	}
	return nil
}

func marshalYAML(v any) ([]byte, error) {
	data, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}

	// Keep each node report self-delimited so callers can append multiple outputs.
	doc := append([]byte("---\n"), data...)
	if len(doc) == 0 || doc[len(doc)-1] != '\n' {
		doc = append(doc, '\n')
	}
	return doc, nil
}
