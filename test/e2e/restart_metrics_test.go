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
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRestartPrepareCounters(t *testing.T) {
	for _, tc := range []struct {
		name      string
		raw       string
		want      map[string]float64
		wantError bool
	}{
		{
			name: "all result counters including zero",
			raw:  "# TYPE dra_cpu_prepare_claims_total counter\ndra_cpu_prepare_claims_total{result=\"success\"} 3\ndra_cpu_prepare_claims_total{result=\"error\"} 0\ndra_cpu_prepare_claims_total{result=\"unknown\"} 0\n",
			want: map[string]float64{"success": 3, "error": 0, "unknown": 0},
		},
		{name: "duplicate result", raw: "# TYPE dra_cpu_prepare_claims_total counter\ndra_cpu_prepare_claims_total{result=\"success\"} 1\ndra_cpu_prepare_claims_total{result=\"success\"} 2\n", wantError: true},
		{name: "unexpected label", raw: "# TYPE dra_cpu_prepare_claims_total counter\ndra_cpu_prepare_claims_total{result=\"success\",extra=\"value\"} 1\n", wantError: true},
		{name: "missing metric", raw: "other_metric 1\n", wantError: true},
		{name: "malformed metric", raw: "not valid prometheus text", wantError: true},
		{name: "wrong type", raw: "# TYPE dra_cpu_prepare_claims_total gauge\ndra_cpu_prepare_claims_total{result=\"success\"} 1\n", wantError: true},
		{name: "missing result label", raw: "# TYPE dra_cpu_prepare_claims_total counter\ndra_cpu_prepare_claims_total 1\n", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := restartPrepareCounters(tc.raw)
			if tc.wantError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.want, got)
		})
	}
}
