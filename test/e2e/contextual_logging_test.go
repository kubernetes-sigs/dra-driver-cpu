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
	"time"

	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/fixture"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/logcheck"
	e2enode "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/node"
	e2epod "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/pod"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
)

// ReportAfterSuite runs after all specs in the suite have completed,
// giving us access to the driver logs produced by the real e2e workload.
// This lets us validate contextual logging invariants (batchID consistency,
// claimUID presence, etc.) against actual log output.
// The alternative would be using a dedicated spec that must be ordered last,
// which seems more fragile and cumbersome.
var _ = ginkgo.ReportAfterSuite("contextual logging", func(report ginkgo.Report) {
	if !report.SuiteSucceeded {
		return
	}

	ctx := context.Background()

	rootFxt, err := fixture.ForGinkgo()
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot create root fixture: %v", err)
	ctxlogFxt := rootFxt.WithPrefix("ctxlog")
	gomega.Expect(ctxlogFxt.Setup(ctx)).To(gomega.Succeed())
	defer func() { gomega.Expect(ctxlogFxt.Teardown(ctx)).To(gomega.Succeed()) }()

	targetNode, err := e2enode.PickWorker(ctx, ctxlogFxt.K8SClientset, 5*time.Second, 1*time.Minute, ctxlogFxt.Log)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())
	ctxlogFxt.Log.Info("using worker node", "nodeName", targetNode.Name)

	dracpuPod, err := e2epod.GetDRACPUPod(ctx, ctxlogFxt.K8SClientset, targetNode.Name)
	gomega.Expect(err).ToNot(gomega.HaveOccurred())

	logs, err := e2epod.GetLogs(ctx, ctxlogFxt.K8SClientset, dracpuPod)
	gomega.Expect(err).ToNot(gomega.HaveOccurred(), "cannot get logs from the dracpu pod: %v", err)

	if envVal, ok := os.LookupEnv("DRACPU_E2E_DUMP_RAW_LOGS"); ok {
		if val, err := strconv.ParseBool(envVal); err == nil && val {
			fmt.Fprintf(ginkgo.GinkgoWriter, "logs:\n%s", logs)
		}
	}

	parsedLog := logcheck.NewParsedLog(logs)
	ctxlogFxt.Log.Info("ingested log lines", "count", parsedLog.Len())

	dupKeys := logcheck.DuplicateKeys(parsedLog)
	if len(dupKeys) > 0 {
		fmt.Fprintf(ginkgo.GinkgoWriter, "lines with duplicated keys:\n%s", logcheck.FormatDuplicateKeyResult(parsedLog, dupKeys...))
	}
	gomega.Expect(dupKeys).To(gomega.BeEmpty(), "found %d lines with duplicated keys", len(dupKeys))

	imbalances := logcheck.ImbalancedFlowTags(parsedLog)
	if len(imbalances) > 0 {
		fmt.Fprintf(ginkgo.GinkgoWriter, "begin/end imbalances:\n%s", logcheck.FormatOperationFlowTagSummary(imbalances...))
	}
	gomega.Expect(imbalances).To(gomega.BeEmpty(), "found %d operations with begin/end imbalance", len(imbalances))

	operations, anomalies := parsedLog.DemuxFlows()
	if len(anomalies) > 0 {
		fmt.Fprintf(ginkgo.GinkgoWriter, "lines with anomalies:\n%s", formatAnomalies(parsedLog, anomalies))
	}
	gomega.Expect(anomalies).To(gomega.BeEmpty(), "found %d lines with invalid opID", len(anomalies))
	ctxlogFxt.Log.Info("detected operations", "count", len(operations))

	for opID, opLines := range operations {
		gomega.Expect(opLines.Indexes).ToNot(gomega.BeEmpty(), "no log entries for operation %q", opID)

		beginMessage, _ := parsedLog.MessageAt(opLines.First())
		gomega.Expect(beginMessage).To(gomega.HavePrefix("begin: "), "operation %q does not start with begin:", opID)

		endMessage, _ := parsedLog.MessageAt(opLines.Last())
		gomega.Expect(endMessage).To(gomega.HavePrefix("end: "), "operation %q does not end with end:", opID)
	}
})

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
