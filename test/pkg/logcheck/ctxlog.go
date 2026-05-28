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
	"bufio"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/sets"
)

var (
	kvPairRE    = regexp.MustCompile(`(\w+)=("([^"]*)"|(\S+))`)
	validOPIDRE = regexp.MustCompile(`^[0-9a-f]+$`)
)

const (
	logUnknown = "unknown"
	logInfo    = "info"
	logError   = "error"
)

const (
	tagBegin = "begin: "
	tagEnd   = "end: "
	tagOPID  = "opID"
)

type parsedLogLine struct {
	// log severity
	severity string
	// the log message (first quoted string after the klog header)
	message string
	// serialized key=value pairs (everything after the quoted message)
	kvPairs string
}

func logSeverityFromRawLine(rawLine string) string {
	if rawLine == "" {
		return logUnknown
	}
	switch rawLine[0] {
	case 'I':
		return logInfo
	case 'E':
		return logError
	// with contextual logging through logr we don't get anything else
	default:
		return logUnknown
	}
}

type ParsedLog struct {
	// OpaqueKeys tell the log parser which keys it should avoid to process.
	// The reason for not processing keys is that we want to implement a minimal
	// log parser for validation purposes. A general purpose structured log parser
	// is a much larger endeavour which has no real usecase yet.
	OpaqueKeys []string
	lines      []parsedLogLine
}

// parseTextloggerPayload splits a payload line in message and kvPairs.
func parseTextLoggerPayload(data string) (string, string) {
	// I0505 17:29:25.356513 112267 dra_hooks.go:269] "begin: preparing resource claims" numClaims=2 batchID="a9ff17853d90"
	// |<---------removed by preprocessor ---------->|<-------------message----------->|<----------kvPairs--------------->|
	if len(data) < 2 || data[0] != '"' {
		return "", data
	}
	end := strings.Index(data[1:], `"`)
	if end < 0 {
		return "", data
	}
	message := data[1 : end+1]
	kvPairs := strings.TrimSpace(data[end+2:])
	return message, kvPairs
}

// ParsedLog holds preprocessed lines emitted by a klog/textlogger
// the header format is stripped (date/time, file...) and we
// retain only the messages payload.
func NewParsedLog(rawLogs string) ParsedLog {
	var lines []parsedLogLine
	sc := bufio.NewScanner(strings.NewReader(rawLogs))
	for sc.Scan() {
		rawLine := sc.Text()
		parts := strings.SplitN(rawLine, "] ", 2)
		if len(parts) != 2 {
			continue
		}
		message, kvPairs := parseTextLoggerPayload(parts[1])
		lines = append(lines, parsedLogLine{
			severity: logSeverityFromRawLine(rawLine),
			message:  message,
			kvPairs:  kvPairs,
		})
	}
	return ParsedLog{
		lines: lines,
	}
}

func (pl ParsedLog) Len() int {
	return len(pl.lines)
}

func (pl ParsedLog) LineAt(idx int) (string, bool) {
	if idx < 0 || idx >= len(pl.lines) {
		return "", false
	}
	line := pl.lines[idx]
	if line.kvPairs == "" {
		return line.message, true
	}
	return line.message + " " + line.kvPairs, true
}

func (pl ParsedLog) MessageAt(idx int) (string, bool) {
	if idx < 0 || idx >= len(pl.lines) {
		return "", false
	}
	return pl.lines[idx].message, true
}

func (pl ParsedLog) KVPairsAt(idx int) [][]string {
	if idx < 0 || idx >= len(pl.lines) {
		return nil
	}
	kvPairs := pl.lines[idx].kvPairs
	for _, sk := range pl.OpaqueKeys {
		kvPairs = RedactQuotedValue(kvPairs, sk)
	}
	return kvPairRE.FindAllStringSubmatch(kvPairs, -1)
}

type FlowEntries struct {
	Indexes []int
}

func (fe FlowEntries) First() int {
	return fe.Indexes[0]
}

func (fe FlowEntries) Last() int {
	return fe.Indexes[len(fe.Indexes)-1]
}

// DemuxFlows groups all the lines pertaining to an operation,
// grouped by opID. Returns a opID->FlowLineIndexes mapping,
// and a linenumber -> error mapping should it encounter
// any anomaly be detected during processing
func (pl ParsedLog) DemuxFlows() (map[string]*FlowEntries, map[int]error) {
	// lineNum -> error
	errs := make(map[int]error)
	// opID -> FlowEntries
	res := make(map[string]*FlowEntries)
	for idx := range pl.lines {
		for _, match := range pl.KVPairsAt(idx) {
			key := match[1]
			if key != tagOPID {
				continue
			}
			val := match[3] // clean unquoted string
			if !IsValidOPID(val) {
				errs[idx] = fmt.Errorf("invalid %s %q at line %d", tagOPID, val, idx)
				continue
			}
			fe, ok := res[val]
			if !ok {
				fe = &FlowEntries{}
				res[val] = fe
			}
			fe.Indexes = append(fe.Indexes, idx)
			break
		}
	}
	return res, errs
}

func IsValidOPID(opID string) bool {
	return len(opID) > 0 && validOPIDRE.MatchString(opID)
}

type OperationFlowTagSummary struct {
	Operation  string
	BeginCount int
	EndCount   int
}

// ImbalancedFlowTags counts begin/end markers per operation name across all lines.
// Returns imbalances where the counts differ.
func ImbalancedFlowTags(pl ParsedLog) []OperationFlowTagSummary {
	begins := make(map[string]int)
	ends := make(map[string]int)
	for _, line := range pl.lines {
		if op, ok := strings.CutPrefix(line.message, tagBegin); ok {
			begins[op]++
			continue
		}
		if op, ok := strings.CutPrefix(line.message, tagEnd); ok {
			ends[op]++
			continue
		}
	}
	ops := sets.New[string]()
	ops.Insert(sets.KeySet(begins).UnsortedList()...)
	ops.Insert(sets.KeySet(ends).UnsortedList()...)

	var res []OperationFlowTagSummary
	for _, op := range sets.List(ops) {
		b, e := begins[op], ends[op]
		if b == e {
			continue
		}
		res = append(res, OperationFlowTagSummary{
			Operation:  op,
			BeginCount: b,
			EndCount:   e,
		})
	}
	return res
}

func FormatOperationFlowTagSummary(imbalances ...OperationFlowTagSummary) string {
	if len(imbalances) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, imb := range imbalances {
		if i > 0 {
			sb.WriteByte('\n')
		}
		fmt.Fprintf(&sb, "operation=%q begin=%d end=%d", imb.Operation, imb.BeginCount, imb.EndCount)
	}
	return sb.String()
}

type DuplicateKeyResult struct {
	Index      int
	Duplicates []string
}

// DuplicateKeys returns lines that have the same KV key appearing more than once.
func DuplicateKeys(pl ParsedLog) []DuplicateKeyResult {
	var res []DuplicateKeyResult
	for idx := range pl.lines {
		dupes := []string{}
		keysPerLine := sets.New[string]()
		for _, match := range pl.KVPairsAt(idx) {
			key := match[1]
			if keysPerLine.Has(key) {
				dupes = append(dupes, key)
			} else {
				keysPerLine.Insert(key)
			}
		}
		if len(dupes) == 0 {
			continue
		}
		res = append(res, DuplicateKeyResult{
			Index:      idx,
			Duplicates: dupes,
		})
	}
	return res
}

func FormatDuplicateKeyResult(parsedLog ParsedLog, dupKeys ...DuplicateKeyResult) string {
	if len(dupKeys) == 0 {
		return ""
	}
	var sb strings.Builder
	rawLine, _ := parsedLog.LineAt(dupKeys[0].Index)
	fmt.Fprintf(&sb, "line=[%s] duplicated keys=<%s>", rawLine, strings.Join(dupKeys[0].Duplicates, ","))
	for _, dupKey := range dupKeys[1:] {
		rawLine, _ := parsedLog.LineAt(dupKey.Index)
		fmt.Fprintf(&sb, "\nline=[%s] duplicated keys=<%s>", rawLine, strings.Join(dupKey.Duplicates, ","))
	}
	return sb.String()
}

const opaqueValue = "@OPAQUE@"

// RedactQuotedValue replaces the value of a key="..." pair from s with a well known generic opaque value.
// Use this function to remove nested response which confuse the log parser.
func RedactQuotedValue(s, key string) string {
	result := ""
	for {
		before, after, found := strings.Cut(s, key+`=`)
		if !found {
			return result + s
		}
		quoted, err := strconv.QuotedPrefix(after)
		if err != nil {
			return result + s
		}
		result += before + key + `="` + opaqueValue + `"`
		s = after[len(quoted):]
	}
}
