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

package e2e

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/logcheck"
	e2epod "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/pod"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	v1 "k8s.io/api/core/v1"
)

// ReportAfterSuite runs after all specs in the suite have completed,
// giving us access to the driver logs produced by the real e2e workload.
// This lets us validate contextual logging invariants (batchID consistency,
// claimUID presence, etc.) against actual log output.
// The alternative would be using a dedicated spec that must be ordered last,
// which seems more fragile and cumbersome.
var _ = ginkgo.ReportAfterSuite("contextual logging", func(report ginkgo.Report) {
	if !report.SuiteSucceeded {
		fmt.Fprintln(ginkgo.GinkgoWriter, "contextual logging audit continuing despite earlier suite failures")
	}

	ctx := context.Background()

	rootFxt, err := fixture.ForGinkgo()
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create root fixture: %v", err)
	ctxlogFxt := rootFxt.WithPrefix("ctxlog")
	gomega.Expect(ctxlogFxt.Setup(ctx)).To(gomega.Succeed())
	defer func() { gomega.Expect(ctxlogFxt.Teardown(ctx)).To(gomega.Succeed()) }()

	driverPods, err := listDriverPods(ctx, ctxlogFxt.K8SClientset)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	gomega.Expect(driverPods).ToNot(gomega.BeEmpty(), "no driver pods found for contextual logging audit")

	sort.Slice(driverPods, func(i, j int) bool {
		if driverPods[i].Spec.NodeName != driverPods[j].Spec.NodeName {
			return driverPods[i].Spec.NodeName < driverPods[j].Spec.NodeName
		}
		return driverPods[i].Name < driverPods[j].Name
	})

	dumpRawLogs := false
	if envVal, ok := os.LookupEnv("DRACPU_E2E_DUMP_RAW_LOGS"); ok {
		if val, err := strconv.ParseBool(envVal); err == nil {
			dumpRawLogs = val
		}
	}

	var logFetchErrors []string
	var duplicateKeyReports []string
	var imbalanceReports []string
	var anomalyReports []string
	var flowBoundaryReports []string

	for _, dracpuPod := range driverPods {
		podKey := identifyDriverPod(dracpuPod)
		logs, err := e2epod.GetLogs(ctx, ctxlogFxt.K8SClientset, &dracpuPod)
		if err != nil {
			logFetchErrors = append(logFetchErrors, fmt.Sprintf("%s: %v", podKey, err))
			continue
		}

		if dumpRawLogs {
			fmt.Fprintf(ginkgo.GinkgoWriter, "logs for %s:\n%s\n", podKey, logs)
		}

		parsedLog := logcheck.NewParsedLog(logs)
		parsedLog.OpaqueKeys = []string{"response"}
		ctxlogFxt.Log.Info("ingested log lines", "pod", podKey, "count", parsedLog.Len())

		dupKeys := logcheck.DuplicateKeys(parsedLog)
		if len(dupKeys) > 0 {
			duplicateKeyReports = append(duplicateKeyReports,
				fmt.Sprintf("%s:\n%s", podKey, logcheck.FormatDuplicateKeyResult(parsedLog, dupKeys...)))
		}

		imbalances := logcheck.ImbalancedFlowTags(parsedLog)
		if len(imbalances) > 0 {
			imbalanceReports = append(imbalanceReports,
				fmt.Sprintf("%s:\n%s", podKey, logcheck.FormatOperationFlowTagSummary(imbalances...)))
		}

		operations, anomalies := parsedLog.DemuxFlows()
		if len(anomalies) > 0 {
			anomalyReports = append(anomalyReports,
				fmt.Sprintf("%s:\n%s", podKey, formatAnomalies(parsedLog, anomalies)))
		}
		ctxlogFxt.Log.Info("detected operations", "pod", podKey, "count", len(operations))

		for opID, opLines := range operations {
			if len(opLines.Indexes) == 0 {
				flowBoundaryReports = append(flowBoundaryReports,
					fmt.Sprintf("%s: operation %q has no log entries", podKey, opID))
				continue
			}

			beginMessage, _ := parsedLog.MessageAt(opLines.First())
			if !strings.HasPrefix(beginMessage, "begin: ") {
				flowBoundaryReports = append(flowBoundaryReports,
					fmt.Sprintf("%s: operation %q does not start with begin: (got %q)", podKey, opID, beginMessage))
			}

			endMessage, _ := parsedLog.MessageAt(opLines.Last())
			if !strings.HasPrefix(endMessage, "end: ") {
				flowBoundaryReports = append(flowBoundaryReports,
					fmt.Sprintf("%s: operation %q does not end with end: (got %q)", podKey, opID, endMessage))
			}
		}
	}

	gomega.Expect(logFetchErrors).To(gomega.BeEmpty(),
		"failed to fetch logs from %d driver pods:\n%s", len(logFetchErrors), strings.Join(logFetchErrors, "\n"))
	gomega.Expect(duplicateKeyReports).To(gomega.BeEmpty(),
		"found duplicated log keys in %d driver pods:\n%s", len(duplicateKeyReports), strings.Join(duplicateKeyReports, "\n\n"))
	gomega.Expect(imbalanceReports).To(gomega.BeEmpty(),
		"found begin/end imbalances in %d driver pods:\n%s", len(imbalanceReports), strings.Join(imbalanceReports, "\n\n"))
	gomega.Expect(anomalyReports).To(gomega.BeEmpty(),
		"found invalid opID lines in %d driver pods:\n%s", len(anomalyReports), strings.Join(anomalyReports, "\n\n"))
	gomega.Expect(flowBoundaryReports).To(gomega.BeEmpty(),
		"found invalid operation boundaries in %d places:\n%s", len(flowBoundaryReports), strings.Join(flowBoundaryReports, "\n"))
})

func identifyDriverPod(pod v1.Pod) string {
	return fmt.Sprintf("%s/%s@%s", pod.Namespace, pod.Name, pod.Spec.NodeName)
}

func formatAnomalies(parsedLog logcheck.ParsedLog, anomalies map[int]error) string {
	lineNums := make([]int, 0, len(anomalies))
	for idx := range anomalies {
		lineNums = append(lineNums, idx)
	}
	sort.Ints(lineNums)

	var sb strings.Builder
	for i, idx := range lineNums {
		if i > 0 {
			sb.WriteByte('\n')
		}
		line, _ := parsedLog.LineAt(idx)
		fmt.Fprintf(&sb, "line %d: %v [%s]", idx, anomalies[idx], line)
	}
	return sb.String()
}
