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

func TestSetOwner(t *testing.T) {
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
				err := bnd.SetOwner(logger, binding.claim, binding.owner.PodUID, binding.owner.ContainerName)
				ok := (err == nil)
				require.Equal(t, ok, binding.expectOK, "setOwner failed for %v", binding)
			}
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
		err := bnd.SetOwner(logger, binding.claim, binding.owner.PodUID, binding.owner.ContainerName)
		require.NoError(t, err)
	}
	require.Equal(t, bnd.Len(), len(bindings))

	bnd.Cleanup(logger, "claim-123", "claim-456", "claim-789")
	require.Equal(t, bnd.Len(), 0)
}
