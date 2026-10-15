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

// Command check-version-refs fails when the documentation names a release older
// than the one in VERSION.
//
// Cutting a release moves VERSION on and leaves the references behind it stale:
// a heading that labels a section with a release, a `--version x.y.z` example,
// and a self-referential URL pinned to a tag are all correct when they are
// written and quietly wrong afterwards. Nothing about the file changes to give
// it away, which is what makes a check worth having rather than a convention.
//
// A reference that names another release on purpose - an upgrade guide, the
// chart appVersion that trails VERSION - is listed in the exemptions below,
// with the reason it is allowed to be there.
//
// Run via 'make check-version-refs' from the repository root.
package main

import (
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"github.com/Masterminds/semver/v3"
)

const (
	versionFile = "VERSION"
	sourceFile  = "tools/check-version-refs/main.go"
)

var versionRe = regexp.MustCompile(`v?[0-9]+\.[0-9]+\.[0-9]+`)

// exemption names a reference that is allowed to name another release. An entry
// with no text exempts every line of the file, and one with text exempts the
// lines that contain it.
type exemption struct {
	path string
	text string
}

// exemptions lists the references that name another release on purpose, with
// the reason each one is here.
var exemptions = []exemption{
	// The upgrade guide exists to name the releases it upgrades from.
	{path: "docs/user/upgrade.md"},
	// The release a migration path starts to apply at, not the current release.
	{path: "docs/dev/testing.md", text: "starting with 0.3.0"},
	// The chart's appVersion trails VERSION on purpose, and this sentence is the
	// one that accounts for it.
	{path: "docs/user/configuration.md", text: "still selects the"},
}

func main() {
	root := flag.String("C", ".", "repository root")
	flag.Parse()

	pathspecs := flag.Args()
	if len(pathspecs) == 0 {
		fmt.Fprintln(os.Stderr, "no document pathspecs provided")
		os.Exit(2)
	}

	code, err := run(*root, pathspecs)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(code)
}

// run reports whether the documentation still names the release in VERSION, and
// returns a non-zero exit code when a reference is stale.
func run(dir string, pathspecs []string) (int, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return 1, err
	}

	version, err := currentVersion(root)
	if err != nil {
		return 1, err
	}
	target, err := targetRelease(version)
	if err != nil {
		return 1, err
	}

	paths, err := documentFiles(root, pathspecs)
	if err != nil {
		return 1, err
	}

	stale := false
	for _, path := range paths {
		fileStale, err := checkFile(root, path, target, version)
		if err != nil {
			return 1, err
		}
		if fileStale {
			stale = true
		}
	}

	if !stale {
		return 0, nil
	}
	fmt.Fprintf(os.Stderr, "Update the reference to %s, or list it in the exemptions in %s with the reason.\n", version, sourceFile)
	return 1, nil
}

// currentVersion reads VERSION, which holds the release the tree is on.
func currentVersion(root string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, versionFile))
	if err != nil {
		return "", err
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, string(b)), nil
}

// targetRelease parses VERSION into the release the documentation must name. A
// VERSION that is not a plain major.minor.patch release is an error: comparing
// against an unparsed target would silently accept every reference.
func targetRelease(version string) (*semver.Version, error) {
	normalized := strings.TrimPrefix(version, "v")
	target, err := semver.StrictNewVersion(normalized)
	if err != nil || target.Prerelease() != "" || target.Metadata() != "" {
		return nil, fmt.Errorf("%s: %q is not a major.minor.patch release", versionFile, version)
	}
	return target, nil
}

// namesOlderRelease reports whether line carries a version literal from an
// earlier release of the same major version. The major-version comparison is
// what keeps the Kubernetes and API releases named in the same documents out of
// the result: v1.35.0 and v1.0.0 sit next to the chart versions this check is
// about, and a "must equal VERSION" rule would fail on all of them.
func namesOlderRelease(line string, target *semver.Version) bool {
	for _, match := range versionRe.FindAllStringIndex(line, -1) {
		if !versionBoundary(line, match[0], match[1]) {
			continue
		}
		literal := line[match[0]:match[1]]
		release, err := semver.StrictNewVersion(strings.TrimPrefix(literal, "v"))
		if err == nil && release.Major() == target.Major() && release.LessThan(target) {
			return true
		}
	}
	return false
}

// versionBoundary keeps a version literal embedded in a longer dotted number,
// such as the first three components of an IPv4 address, from being treated as
// a release reference.
func versionBoundary(line string, start, end int) bool {
	if start > 0 && (isASCIIDigit(line[start-1]) || (line[start-1] == '.' && start > 1 && isASCIIDigit(line[start-2]))) {
		return false
	}
	if end < len(line) && (isASCIIDigit(line[end]) || (line[end] == '.' && end+1 < len(line) && isASCIIDigit(line[end+1]))) {
		return false
	}
	return true
}

func isASCIIDigit(c byte) bool {
	return c >= '0' && c <= '9'
}

// exempt reports whether an exemption lets this line name another release.
func exempt(path, line string) bool {
	for _, entry := range exemptions {
		if entry.path == path && (entry.text == "" || strings.Contains(line, entry.text)) {
			return true
		}
	}
	return false
}

// documentFiles lists the tracked documents matching the provided pathspecs.
// Paths come back relative to the repository root, so the message reads the
// same in a local run and in a CI log.
func documentFiles(root string, pathspecs []string) ([]string, error) {
	args := append([]string{"-C", root, "ls-files", "--"}, pathspecs...)
	out, err := exec.Command("git", args...).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("git ls-files: %s", bytes.TrimSpace(exitErr.Stderr))
		}
		return nil, fmt.Errorf("git ls-files: %w", err)
	}
	var paths []string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		if path := scanner.Text(); path != "" {
			paths = append(paths, path)
		}
	}
	return paths, scanner.Err()
}

func checkFile(root, path string, target *semver.Version, version string) (bool, error) {
	file, err := os.Open(filepath.Join(root, path))
	if err != nil {
		return false, err
	}
	defer file.Close()

	stale := false
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, bufio.MaxScanTokenSize), 1024*1024)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := scanner.Text()
		if !namesOlderRelease(line, target) || exempt(path, line) {
			continue
		}
		diagnostic := fmt.Sprintf("%s:%d names a release older than %s:\n    %s\n", path, lineNumber, version, line)
		if _, err := os.Stderr.WriteString(diagnostic); err != nil {
			return false, fmt.Errorf("write diagnostic: %w", err)
		}
		stale = true
	}
	if err := scanner.Err(); err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}
	return stale, nil
}
