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

package client_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/client"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
)

func TestListNamespacedObjects(t *testing.T) {
	pod := client.ObjectRef{
		GVR: schema.GroupVersionResource{
			Version:  "v1",
			Resource: "pods",
		},
		Namespace: "test",
		Name:      "pod",
		UID:       "pod-uid",
	}
	claim := client.ObjectRef{
		GVR: schema.GroupVersionResource{
			Group:    "resource.k8s.io",
			Version:  "v1",
			Resource: "resourceclaims",
		},
		Namespace: "test",
		Name:      "claim",
		UID:       "claim-uid",
	}
	template := client.ObjectRef{
		GVR: schema.GroupVersionResource{
			Group:    "resource.k8s.io",
			Version:  "v1",
			Resource: "resourceclaimtemplates",
		},
		Namespace: "test",
		Name:      "template",
		UID:       "template-uid",
	}

	testCases := []struct {
		name      string
		namespace string
		labels    map[string]string
		want      []client.ObjectRef
	}{
		{
			name:      "all wrapped namespaced types",
			namespace: "test",
			labels:    map[string]string{"owner": "suite"},
			want:      []client.ObjectRef{pod, claim, template},
		},
		{
			name:      "all label pairs must match",
			namespace: "test",
			labels:    map[string]string{"owner": "suite", "fixture": "one"},
			want:      []client.ObjectRef{pod},
		},
		{
			name:      "different value",
			namespace: "test",
			labels:    map[string]string{"owner": "other"},
		},
		{
			name:      "missing label",
			namespace: "test",
			labels:    map[string]string{"missing": "suite"},
		},
		{
			name:      "empty label value",
			namespace: "test",
			labels:    map[string]string{"empty": ""},
			want:      []client.ObjectRef{pod},
		},
		{
			name:      "empty namespace contents",
			namespace: "unused",
			labels:    map[string]string{"owner": "suite"},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cs := fake.NewClientset(
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "pod",
						Namespace: "test",
						UID:       "pod-uid",
						Labels: map[string]string{
							"owner":   "suite",
							"fixture": "one",
							"empty":   "",
						},
					},
				},
				&resourcev1.ResourceClaim{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "claim",
						Namespace: "test",
						UID:       "claim-uid",
						Labels: map[string]string{
							"owner":   "suite",
							"fixture": "two",
						},
					},
				},
				&resourcev1.ResourceClaimTemplate{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "template",
						Namespace: "test",
						UID:       "template-uid",
						Labels: map[string]string{
							"owner": "suite",
						},
					},
				},
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "unrelated",
						Namespace: "test",
					},
				},
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "elsewhere",
						Namespace: "other",
						Labels: map[string]string{
							"owner": "suite",
						},
					},
				},
				&corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{
						Name: "test",
						Labels: map[string]string{
							"owner": "suite",
						},
					},
				},
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "unwrapped",
						Namespace: "test",
						Labels: map[string]string{
							"owner": "suite",
						},
					},
				},
			)
			got, err := client.ListNamespacedObjectsWithLabels(t.Context(), cs, tc.namespace, tc.labels)
			if err != nil {
				t.Fatalf("list objects: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Fatalf("object references mismatch (-want +got):\n%s", diff)
			}
			if len(cs.Actions()) != 3 {
				t.Fatalf("got %d API calls, want exactly three namespaced lists", len(cs.Actions()))
			}
			wantResources := []schema.GroupVersionResource{pod.GVR, claim.GVR, template.GVR}
			for i, action := range cs.Actions() {
				if action.GetVerb() != "list" || action.GetNamespace() != tc.namespace || action.GetResource() != wantResources[i] {
					t.Fatalf("unexpected API call: %#v", action)
				}
			}
		})
	}
}

func TestListNamespacedObjectsRejectsEmptyNamespace(t *testing.T) {
	testCases := []struct {
		name          string
		defaultLabels bool
	}{
		{
			name:          "default labels",
			defaultLabels: true,
		},
		{
			name: "explicit labels",
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cs := fake.NewClientset()
			var refs []client.ObjectRef
			var err error
			if tc.defaultLabels {
				refs, err = client.ListNamespacedObjects(t.Context(), cs, "")
			} else {
				refs, err = client.ListNamespacedObjectsWithLabels(t.Context(), cs, "", map[string]string{"owner": "suite"})
			}
			if err == nil {
				t.Fatal("empty namespace accepted; want an error")
			}
			if refs != nil {
				t.Fatalf("got references %v, want nil", refs)
			}
			if len(cs.Actions()) != 0 {
				t.Fatalf("empty namespace triggered API calls: %v", cs.Actions())
			}
		})
	}
}
