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

package logcheck

import (
	"testing"
)

const ciLogSnippet = `"claims:{key:\"8c03a869-1521-42d0-b73a-aec1a070375f\" value:{devices:{request_names:\"request-0\" pool_name:\"dra-driver-cpu-worker\" device_name:\"cpudevnuma000\" cdi_device_ids:\"dra.k8s.io/cpu=claim-8c03a869-1521-42d0-b73a-aec1a070375f\"} devices:{request_names:\"request-1\" pool_name:\"dra-driver-cpu-worker\" device_name:\"cpudevnuma000\" cdi_device_ids:\"dra.k8s.io/cpu=claim-8c03a869-1521-42d0-b73a-aec1a070375f\"}}}"`

func TestRedactQuotedValue(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		key      string
		expected string
	}{
		{
			name:     "key not present",
			input:    `logger="dra" method="foo"`,
			key:      "response",
			expected: `logger="dra" method="foo"`,
		},
		{
			name:     "simple value no escaped quotes",
			input:    `logger="dra" response="ok" method="foo"`,
			key:      "response",
			expected: `logger="dra" response="@OPAQUE@" method="foo"`,
		},
		{
			name:     "simple value no escaped quotes, multiple occurrences",
			input:    `logger="dra" response="ok" method="foo" response="good"`,
			key:      "response",
			expected: `logger="dra" response="@OPAQUE@" method="foo" response="@OPAQUE@"`,
		},
		{
			name:     "quoted values in the response kv pair from CI",
			input:    `logger="dra" requestID=10 method="/k8s.io.kubelet.pkg.apis.dra.v1.DRAPlugin/NodePrepareResources" response=` + ciLogSnippet,
			key:      "response",
			expected: `logger="dra" requestID=10 method="/k8s.io.kubelet.pkg.apis.dra.v1.DRAPlugin/NodePrepareResources" response="@OPAQUE@"`,
		},
		{
			name:     "quoted values in the response kv pair from CI, multiple occurrences",
			input:    `logger="dra" requestID=10 method="/k8s.io.kubelet.pkg.apis.dra.v1.DRAPlugin/NodePrepareResources" response=` + ciLogSnippet + ` response=` + ciLogSnippet,
			key:      "response",
			expected: `logger="dra" requestID=10 method="/k8s.io.kubelet.pkg.apis.dra.v1.DRAPlugin/NodePrepareResources" response="@OPAQUE@" response="@OPAQUE@"`,
		},

		{
			name:     "single key is only occurrence",
			input:    `response="value"`,
			key:      "response",
			expected: `response="@OPAQUE@"`,
		},
		{
			name:     "multiple keys are the only occurrence",
			input:    `response="value1" response="value2"`,
			key:      "response",
			expected: `response="@OPAQUE@" response="@OPAQUE@"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := RedactQuotedValue(tt.input, tt.key)
			if got != tt.expected {
				t.Errorf("RedactQuotedValue(%q, %q)\ngot:  %q\nwant: %q", tt.input, tt.key, got, tt.expected)
			}
		})
	}
}

// reconstructed from the CI snippet
var ciLogLine = `I0520 12:51:01.000000 1 nonblockinggrpcserver.go:164] "handling request succeeded" logger="dra" requestID=10 method="/k8s.io.kubelet.pkg.apis.dra.v1.DRAPlugin/NodePrepareResources" response=` + ciLogSnippet + "\n"

/*
reproduces the issue in CI - https://github.com/kubernetes-sigs/dra-driver-cpu/pull/150#pullrequestreview-4329037772
---
[ReportAfterSuite] contextual logging
/home/prow/go/src/sigs.k8s.io/dra-driver-cpu/test/e2e/contextual_logging_test.go:42

	"level"=0 "msg"="fixture setup" "namespace"="dracpu-e2e-ctxlog-hddj6"
	"level"=0 "msg"="using worker node" "nodeName"="dra-driver-cpu-worker"
	"level"=0 "msg"="ingested log lines" "count"=238
	lines with duplicated keys:
	line=[handling request succeeded logger="dra" requestID=10 method="/k8s.io.kubelet.pkg.apis.dra.v1.DRAPlugin/NodePrepareResources" response="claims:{key:\"8c03a869-1521-42d0-b73a-aec1a070375f\" value:{devices:{request_names:\"request-0\" pool_name:\"dra-driver-cpu-worker\" device_name:\"cpudevnuma000\" cdi_device_ids:\"dra.k8s.io/cpu=claim-8c03a869-1521-42d0-b73a-aec1a070375f\"} devices:{request_names:\"request-1\" pool_name:\"dra-driver-cpu-worker\" device_name:\"cpudevnuma000\" cdi_device_ids:\"dra.k8s.io/cpu=claim-8c03a869-1521-42d0-b73a-aec1a070375f\"}}}"] duplicated keys=<cpu>"level"=0 "msg"="fixture teardown" "namespace"="dracpu-e2e-ctxlog-hddj6"
	[FAILED] in [ReportAfterSuite] - /home/prow/go/src/sigs.k8s.io/dra-driver-cpu/test/e2e/contextual_logging_test.go:78 @ 05/20/26 12:51:02.094

[ReportAfterSuite] [FAILED] [15.027 seconds]
[ReportAfterSuite] contextual logging
/home/prow/go/src/sigs.k8s.io/dra-driver-cpu/test/e2e/contextual_logging_test.go:42

	[FAILED] found 1 lines with duplicated keys
	Expected
	    <[]logcheck.DuplicateKeyResult | len:1, cap:1>: [
	        {Index: 218, Duplicates: ["cpu"]},
	    ]
	to be empty
	In [ReportAfterSuite] at: /home/prow/go/src/sigs.k8s.io/dra-driver-cpu/test/e2e/contextual_logging_test.go:78 @ 05/20/26 12:51:02.094

---
*/
func TestDuplicateKeysWithoutSkipFalsePositive(t *testing.T) {
	pl := NewParsedLog(ciLogLine)
	dupes := DuplicateKeys(pl)
	if len(dupes) == 0 {
		t.Fatal("expected false-positive duplicate detection without declared opaque keys")
	}
	found := false
	for _, d := range dupes[0].Duplicates {
		if d == "cpu" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'cpu' in duplicates, got %v", dupes[0].Duplicates)
	}
}

// verification of the CI issue - https://github.com/kubernetes-sigs/dra-driver-cpu/pull/150#pullrequestreview-4329037772
func TestDuplicateKeysHandlesNestedQuotesWithSkipAvoidsFalsePositive(t *testing.T) {
	pl := NewParsedLog(ciLogLine)
	pl.OpaqueKeys = []string{"response"}
	dupes := DuplicateKeys(pl)
	if len(dupes) != 0 {
		t.Errorf("expected no duplicates with declared opaque keys, got %v", dupes)
	}
}

func TestDuplicateKeysWithSkipDetectsRealDuplicate(t *testing.T) {
	realDupLog := `I0520 12:51:01.000000 1 nonblockinggrpcserver.go:164] "msg" response=` + ciLogSnippet + ` response=` + ciLogSnippet + "\n"
	pl := NewParsedLog(realDupLog)
	pl.OpaqueKeys = []string{"response"}
	dupes := DuplicateKeys(pl)
	if len(dupes) != 1 {
		t.Fatalf("expected 1 line with duplicates, got %d", len(dupes))
	}
	if dupes[0].Duplicates[0] != "response" {
		t.Errorf("expected 'response' duplicate, got %v", dupes[0].Duplicates)
	}
}
