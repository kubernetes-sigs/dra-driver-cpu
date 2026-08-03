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

package store

import (
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
)

type binding struct {
	claim    k8stypes.UID
	owner    OwnerIdent
	expectOK bool
}

func TestSetOwnerSequential(t *testing.T) {
	type testCase struct {
		name     string
		bindings []binding
	}

	testcases := []testCase{
		{
			name: "single binding",
			bindings: []binding{
				{
					claim: k8stypes.UID("claim-123"),
					owner: OwnerIdent{
						PodUID:        "pod-AAA",
						ContainerName: "cnt-1",
					},
					expectOK: true,
				},
			},
		},
		{
			name: "binding and rebinding",
			bindings: []binding{
				{
					claim: k8stypes.UID("claim-123"),
					owner: OwnerIdent{
						PodUID:        "pod-AAA",
						ContainerName: "cnt-1",
					},
					expectOK: true,
				},
				{
					claim: k8stypes.UID("claim-123"),
					owner: OwnerIdent{
						PodUID:        "pod-AAA",
						ContainerName: "cnt-1",
					},
					expectOK: true,
				},
			},
		},
		{
			name: "multiple different binding",
			bindings: []binding{
				{
					claim: k8stypes.UID("claim-123"),
					owner: OwnerIdent{
						PodUID:        "pod-AAA",
						ContainerName: "cnt-1",
					},
					expectOK: true,
				},
				{
					claim: k8stypes.UID("claim-456"),
					owner: OwnerIdent{
						PodUID:        "pod-BBB",
						ContainerName: "cnt-1",
					},
					expectOK: true,
				},
			},
		},
		{
			name: "duplicate binding - pod",
			bindings: []binding{
				{
					claim: k8stypes.UID("claim-123"),
					owner: OwnerIdent{
						PodUID:        "pod-AAA",
						ContainerName: "cnt-1",
					},
					expectOK: true,
				},
				{
					claim: k8stypes.UID("claim-123"),
					owner: OwnerIdent{
						PodUID:        "pod-BBB",
						ContainerName: "cnt-1",
					},
					expectOK: false,
				},
			},
		},
		{
			name: "duplicate binding - container",
			bindings: []binding{
				{
					claim: k8stypes.UID("claim-123"),
					owner: OwnerIdent{
						PodUID:        "pod-AAA",
						ContainerName: "cnt-1",
					},
					expectOK: true,
				},
				{
					claim: k8stypes.UID("claim-123"),
					owner: OwnerIdent{
						PodUID:        "pod-AAA",
						ContainerName: "cnt-2",
					},
					expectOK: false,
				},
			},
		},
	}

	for _, tcase := range testcases {
		t.Run(tcase.name, func(t *testing.T) {
			logger := testr.New(t)
			bnd := NewClaimTracker()
			for _, binding := range tcase.bindings {
				_, err := bnd.SetOwner(logger, binding.owner.PodUID, binding.owner.ContainerName, binding.claim)
				ok := (err == nil)
				require.Equal(t, binding.expectOK, ok, "setOwner failed for %v", binding)
			}
		})
	}
}

func TestSetOwnerAtomic(t *testing.T) {
	logger := testr.New(t)
	owner := OwnerIdent{PodUID: "pod-AAA", ContainerName: "cnt-1"}
	testCases := []struct {
		name            string
		initialBindings []binding
		claimUIDs       []k8stypes.UID
		expectedNew     []k8stypes.UID
		expectedLen     int
		expectError     bool
	}{
		{
			name:        "binds all claims atomically",
			claimUIDs:   []k8stypes.UID{"claim-1", "claim-2"},
			expectedNew: []k8stypes.UID{"claim-1", "claim-2"},
			expectedLen: 2,
		},
		{
			name: "conflict leaves new claims unbound",
			initialBindings: []binding{{
				claim: "claim-2",
				owner: OwnerIdent{PodUID: "pod-BBB", ContainerName: "cnt-2"},
			}},
			claimUIDs:   []k8stypes.UID{"claim-1", "claim-2"},
			expectedLen: 1,
			expectError: true,
		},
		{
			name: "returns only newly bound claims",
			initialBindings: []binding{{
				claim: "claim-1",
				owner: owner,
			}},
			claimUIDs:   []k8stypes.UID{"claim-1", "claim-2"},
			expectedNew: []k8stypes.UID{"claim-2"},
			expectedLen: 2,
		},
		{
			name:        "rejects empty claim list",
			expectedLen: 0,
			expectError: true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			tracker := NewClaimTracker()
			for _, initial := range tc.initialBindings {
				_, err := tracker.SetOwner(logger, initial.owner.PodUID, initial.owner.ContainerName, initial.claim)
				require.NoError(t, err)
			}

			newlyBound, err := tracker.SetOwner(logger, owner.PodUID, owner.ContainerName, tc.claimUIDs...)
			if tc.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.ElementsMatch(t, tc.expectedNew, newlyBound)
			require.Equal(t, tc.expectedLen, tracker.Len())
		})
	}
}

func TestLen(t *testing.T) {
	logger := testr.New(t)
	bindings := []binding{
		{
			claim: k8stypes.UID("claim-123"),
			owner: OwnerIdent{
				PodUID:        "pod-AAA",
				ContainerName: "cnt-1",
			},
			expectOK: true,
		},
		{
			claim: k8stypes.UID("claim-456"),
			owner: OwnerIdent{
				PodUID:        "pod-BBB",
				ContainerName: "cnt-1",
			},
			expectOK: true,
		},
		{
			claim: k8stypes.UID("claim-789"),
			owner: OwnerIdent{
				PodUID:        "pod-CCC",
				ContainerName: "cnt-1",
			},
			expectOK: true,
		},
	}

	bnd := NewClaimTracker()
	for _, binding := range bindings {
		_, err := bnd.SetOwner(logger, binding.owner.PodUID, binding.owner.ContainerName, binding.claim)
		require.NoError(t, err)
	}
	require.Equal(t, bnd.Len(), len(bindings))

	bnd.Cleanup("claim-123", "claim-456", "claim-789")
	require.Equal(t, bnd.Len(), 0)
}
