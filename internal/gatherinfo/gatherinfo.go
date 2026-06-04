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
	"strings"
	"time"

	"github.com/go-logr/logr"
	driverconfig "github.com/kubernetes-sigs/dra-driver-cpu/cmd/dracpu/config"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/buildinfo"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/cpuinfo"
	"sigs.k8s.io/yaml"
)

const LayoutVersion = "v1"

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
	DriverConfig      driverconfig.Config
	DriverCmdlinePath string
	Logger            logr.Logger
}

func Run(args []string, opts Options) error {
	fs := flag.NewFlagSet("dracpu-gatherinfo", flag.ExitOnError)
	outputDir := fs.String("output-dir", opts.OutputParentDir, "Parent directory for debug artifacts (default: temporary directory)")
	emitStdout := fs.Bool("stdout", false, "Write the YAML report to stdout instead of a file")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if *emitStdout && *outputDir != "" {
		return fmt.Errorf("--stdout and --output-dir cannot be used together")
	}

	logger := opts.Logger
	if !logger.Enabled() {
		logger = logr.Discard()
	}

	report, err := collectReport(logger, opts.DriverConfig, opts.driverCmdlinePath())
	if err != nil {
		return err
	}

	if *emitStdout {
		return writeYAML(os.Stdout, report)
	}

	outputPath, err := prepareOutputPath(*outputDir, time.Now(), os.Getpid())
	if err != nil {
		return err
	}
	if err := writeReportFile(outputPath, report); err != nil {
		return err
	}

	fmt.Printf("Debug info collected: %s\n", outputPath)
	return nil
}

func (o Options) driverCmdlinePath() string {
	if o.DriverCmdlinePath != "" {
		return o.DriverCmdlinePath
	}
	return defaultDriverCmdlinePath()
}

func defaultDriverCmdlinePath() string {
	return cpuinfo.GetEnv("HOST_ROOT", "/", "proc/1/cmdline")
}

func prepareOutputPath(parentDir string, now time.Time, pid int) (string, error) {
	fileName := fmt.Sprintf("dracpu-gatherinfo-%s-%d.yaml", now.Format("20060102-150405"), pid)
	if parentDir == "" {
		f, err := os.CreateTemp("", "dracpu-gatherinfo-*.yaml")
		if err != nil {
			return "", fmt.Errorf("failed to create temp file: %w", err)
		}
		if err := f.Close(); err != nil {
			return "", fmt.Errorf("failed to close temp file %s: %w", f.Name(), err)
		}
		return f.Name(), nil
	}

	parentDir = filepath.Clean(parentDir)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create output dir: %w", err)
	}

	outputPath := filepath.Join(parentDir, fileName)
	if _, err := os.Stat(outputPath); err == nil {
		return "", fmt.Errorf("output file already exists: %s", outputPath)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("failed to stat output file %s: %w", outputPath, err)
	}

	return outputPath, nil
}

func collectReport(logger logr.Logger, defaults driverconfig.Config, driverCmdlinePath string) (Report, error) {
	sys := cpuinfo.NewSystemCPUInfo()

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
		DriverConfig: detectDriverConfig(defaults, driverCmdlinePath),
	}, nil
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

func detectDriverConfig(defaults driverconfig.Config, driverCmdlinePath string) driverconfig.Config {
	cmdline, err := readCmdlineFile(driverCmdlinePath)
	if err != nil || len(cmdline) == 0 {
		return defaults
	}

	if filepath.Base(cmdline[0]) != "dracpu" {
		return defaults
	}

	cfg := defaults
	fs := flag.NewFlagSet("detect-driver-config", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	cfg.AddFlags(fs)
	// The driver command line can include logging and other shared flags.
	// Re-parse only the driver config flags so those unrelated flags do not
	// make diagnostics fall back to defaults.
	if err := fs.Parse(knownConfigArgs(fs, cmdline[1:])); err != nil {
		return defaults
	}

	return cfg
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

func writeReportFile(path string, v any) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", path, err)
	}

	err = writeYAML(f, v)
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
