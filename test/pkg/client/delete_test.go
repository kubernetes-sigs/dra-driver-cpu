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

	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/client"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestDeleteNamespacedObjects(t *testing.T) {
	const namespace = "test"

	podsGVR := corev1.SchemeGroupVersion.WithResource("pods")
	claimsGVR := resourcev1.SchemeGroupVersion.WithResource("resourceclaims")
	templatesGVR := resourcev1.SchemeGroupVersion.WithResource("resourceclaimtemplates")
	ownedLabels := map[string]string{client.TestResourceLabelKey: client.TestResourceLabelValue}

	testCases := []struct {
		name          string
		objects       []runtime.Object
		wantDone      bool
		wantDeleteUID map[schema.GroupVersionResource]map[string]types.UID
	}{
		{
			name: "no labeled objects",
			objects: []runtime.Object{
				&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unrelated", Namespace: namespace}},
				&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "elsewhere", Namespace: "other", Labels: ownedLabels}},
			},
			wantDone: true,
		},
		{
			name: "delete each labeled object type",
			objects: []runtime.Object{
				&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: namespace, UID: "pod-uid", Labels: ownedLabels}},
				&resourcev1.ResourceClaim{ObjectMeta: metav1.ObjectMeta{Name: "claim", Namespace: namespace, UID: "claim-uid", Labels: ownedLabels}},
				&resourcev1.ResourceClaimTemplate{ObjectMeta: metav1.ObjectMeta{Name: "template", Namespace: namespace, UID: "template-uid", Labels: ownedLabels}},
			},
			wantDeleteUID: map[schema.GroupVersionResource]map[string]types.UID{
				podsGVR:      {"pod": "pod-uid"},
				claimsGVR:    {"claim": "claim-uid"},
				templatesGVR: {"template": "template-uid"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			cs := fake.NewClientset(tc.objects...)

			done, err := client.DeleteNamespacedObjects(t.Context(), cs, namespace)
			if err != nil {
				t.Fatalf("delete namespaced objects: %v", err)
			}
			if done != tc.wantDone {
				t.Fatalf("done = %t, want %t", done, tc.wantDone)
			}

			deleteActions := assertDeleteActions(t, cs.Actions(), tc.wantDeleteUID)
			if tc.wantDone {
				if deleteActions != 0 {
					t.Fatalf("got %d delete actions, want none", deleteActions)
				}
				return
			}

			if deleteActions != 3 {
				t.Fatalf("got %d delete actions, want 3", deleteActions)
			}

			done, err = client.DeleteNamespacedObjects(t.Context(), cs, namespace)
			if err != nil {
				t.Fatalf("check cleanup completion: %v", err)
			}
			if !done {
				t.Fatal("second delete pass reported objects still present")
			}
			if got := assertDeleteActions(t, cs.Actions(), tc.wantDeleteUID); got != 3 {
				t.Fatalf("got %d delete actions after second pass, want 3", got)
			}
		})
	}
}

func TestDeleteNamespacedObjectsRejectsEmptyNamespace(t *testing.T) {
	cs := fake.NewClientset()
	done, err := client.DeleteNamespacedObjects(t.Context(), cs, "")
	if err == nil {
		t.Fatal("empty namespace accepted; want an error")
	}
	if done {
		t.Fatal("empty namespace reported cleanup complete")
	}
	if len(cs.Actions()) != 0 {
		t.Fatalf("empty namespace triggered API calls: %v", cs.Actions())
	}
}

func assertDeleteActions(t *testing.T, actions []k8stesting.Action, want map[schema.GroupVersionResource]map[string]types.UID) int {
	t.Helper()
	got := 0
	for _, action := range actions {
		if action.GetVerb() != "delete" {
			continue
		}
		got++
		deleteAction, ok := action.(k8stesting.DeleteAction)
		if !ok {
			t.Fatalf("delete action has unexpected type %T", action)
		}
		wantNames, ok := want[action.GetResource()]
		if !ok {
			t.Errorf("unexpected delete resource %s", action.GetResource())
			continue
		}
		wantUID, ok := wantNames[deleteAction.GetName()]
		if !ok {
			t.Errorf("unexpected delete name %q for %s", deleteAction.GetName(), action.GetResource())
			continue
		}
		options := deleteAction.GetDeleteOptions()
		if options.Preconditions == nil || options.Preconditions.UID == nil {
			t.Errorf("delete %s %q has no UID precondition", action.GetResource(), deleteAction.GetName())
			continue
		}
		if *options.Preconditions.UID != wantUID {
			t.Errorf("delete %s %q UID precondition = %q, want %q", action.GetResource(), deleteAction.GetName(), *options.Preconditions.UID, wantUID)
		}
	}
	return got
}
