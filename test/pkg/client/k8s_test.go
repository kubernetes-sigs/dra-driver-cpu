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

package client

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

// These tests use a fake clientset, which implements kubernetes.Interface
// directly and never goes through the HTTP transport.
// Therefore we can't test the roudntripper auditor through it, it would
// require an integration test (e.g. httptest) which is deferred for now

func TestCreateInjectsTestResourceLabel(t *testing.T) {
	const namespace = "test"

	testCases := []struct {
		name     string
		resource string
		create   func(context.Context, kubernetes.Interface) (metav1.Object, error)
		read     func(context.Context, kubernetes.Interface) (metav1.Object, error)
	}{
		{
			name:     "pod",
			resource: "pods",
			create: func(ctx context.Context, cs kubernetes.Interface) (metav1.Object, error) {
				return cs.CoreV1().Pods(namespace).Create(ctx, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "pod"},
				}, metav1.CreateOptions{})
			},
			read: func(ctx context.Context, cs kubernetes.Interface) (metav1.Object, error) {
				return cs.CoreV1().Pods(namespace).Get(ctx, "pod", metav1.GetOptions{})
			},
		},
		{
			name: "namespace",
			create: func(ctx context.Context, cs kubernetes.Interface) (metav1.Object, error) {
				return cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
					ObjectMeta: metav1.ObjectMeta{Name: "namespace"},
				}, metav1.CreateOptions{})
			},
			read: func(ctx context.Context, cs kubernetes.Interface) (metav1.Object, error) {
				return cs.CoreV1().Namespaces().Get(ctx, "namespace", metav1.GetOptions{})
			},
		},
		{
			name:     "resource claim",
			resource: "resourceclaims",
			create: func(ctx context.Context, cs kubernetes.Interface) (metav1.Object, error) {
				return cs.ResourceV1().ResourceClaims(namespace).Create(ctx, &resourcev1.ResourceClaim{
					ObjectMeta: metav1.ObjectMeta{Name: "claim"},
				}, metav1.CreateOptions{})
			},
			read: func(ctx context.Context, cs kubernetes.Interface) (metav1.Object, error) {
				return cs.ResourceV1().ResourceClaims(namespace).Get(ctx, "claim", metav1.GetOptions{})
			},
		},
		{
			name:     "resource claim template",
			resource: "resourceclaimtemplates",
			create: func(ctx context.Context, cs kubernetes.Interface) (metav1.Object, error) {
				return cs.ResourceV1().ResourceClaimTemplates(namespace).Create(ctx, &resourcev1.ResourceClaimTemplate{
					ObjectMeta: metav1.ObjectMeta{Name: "template"},
				}, metav1.CreateOptions{})
			},
			read: func(ctx context.Context, cs kubernetes.Interface) (metav1.Object, error) {
				return cs.ResourceV1().ResourceClaimTemplates(namespace).Get(ctx, "template", metav1.GetOptions{})
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			cs, err := NewK8SClientset(WithClientset(fake.NewClientset()))
			if err != nil {
				t.Fatalf("cannot create clientset: %v", err)
			}

			created, err := tc.create(ctx, cs)
			if err != nil {
				t.Fatalf("cannot create object: %v", err)
			}
			if got := created.GetLabels()[TestResourceLabelKey]; got != TestResourceLabelValue {
				t.Fatalf("label %q = %q, want %q", TestResourceLabelKey, got, TestResourceLabelValue)
			}

			stored, err := tc.read(ctx, cs)
			if err != nil {
				t.Fatalf("cannot read created object: %v", err)
			}
			if got := stored.GetLabels()[TestResourceLabelKey]; got != TestResourceLabelValue {
				t.Fatalf("label %q = %q, want %q", TestResourceLabelKey, got, TestResourceLabelValue)
			}

			refs, err := ListNamespacedObjects(ctx, cs, namespace)
			if err != nil {
				t.Fatalf("list labeled objects: %v", err)
			}
			if tc.resource == "" {
				if len(refs) != 0 {
					t.Fatalf("cluster-scoped object included in namespaced list: %v", refs)
				}
				return
			}
			if len(refs) != 1 {
				t.Fatalf("got %v, want one reference to the wrapped create result", refs)
			}
			if ref := refs[0]; ref.GVR.Resource != tc.resource || ref.Namespace != namespace || ref.Name != created.GetName() || ref.UID != created.GetUID() {
				t.Fatalf("unexpected reference to wrapped create result: %+v", ref)
			}
		})
	}
}

func TestUpdateDoesNotInjectTestResourceLabel(t *testing.T) {
	ctx := context.Background()
	cs, err := NewK8SClientset(WithClientset(fake.NewClientset()))
	if err != nil {
		t.Fatalf("cannot create clientset: %v", err)
	}
	pods := cs.CoreV1().Pods("test")

	created, err := pods.Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "pod",
			Labels: map[string]string{"app": "test"},
		},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("cannot create pod: %v", err)
	}
	if got := created.Labels["app"]; got != "test" {
		t.Fatalf("label app = %q, want %q", got, "test")
	}
	if got := created.Labels[TestResourceLabelKey]; got != TestResourceLabelValue {
		t.Fatalf("label %q = %q, want %q", TestResourceLabelKey, got, TestResourceLabelValue)
	}
	delete(created.Labels, TestResourceLabelKey)

	updated, err := pods.Update(ctx, created, metav1.UpdateOptions{})
	if err != nil {
		t.Fatalf("cannot update pod: %v", err)
	}
	if _, ok := updated.Labels[TestResourceLabelKey]; ok {
		t.Fatalf("update added label %q", TestResourceLabelKey)
	}
	if got := updated.Labels["app"]; got != "test" {
		t.Fatalf("label app = %q, want %q", got, "test")
	}
}

func TestWithoutCreateLabelInjection(t *testing.T) {
	ctx := context.Background()
	cs, err := NewK8SClientset(WithoutCreateLabelInjection(), WithClientset(fake.NewClientset()))
	if err != nil {
		t.Fatalf("cannot create clientset: %v", err)
	}

	created, err := cs.CoreV1().Pods("test").Create(ctx, &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod"},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatalf("cannot create pod: %v", err)
	}
	if _, ok := created.Labels[TestResourceLabelKey]; ok {
		t.Fatalf("create added label %q", TestResourceLabelKey)
	}
}

func TestResourceClaimTemplateCreateLabels(t *testing.T) {
	testCases := []struct {
		name     string
		labels   map[string]string
		disabled bool
		want     map[string]string
	}{
		{
			name: "nil labels",
			want: map[string]string{TestResourceLabelKey: TestResourceLabelValue},
		},
		{
			name:   "preserve existing labels",
			labels: map[string]string{"app": "test"},
			want:   map[string]string{"app": "test", TestResourceLabelKey: TestResourceLabelValue},
		},
		{
			name:   "replace ownership label",
			labels: map[string]string{TestResourceLabelKey: "other"},
			want:   map[string]string{TestResourceLabelKey: TestResourceLabelValue},
		},
		{
			name:     "disabled injection",
			labels:   map[string]string{"app": "test"},
			disabled: true,
			want:     map[string]string{"app": "test"},
		},
	}
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			options := []Option{WithClientset(fake.NewClientset())}
			if tc.disabled {
				options = append(options, WithoutCreateLabelInjection())
			}
			cs, err := NewK8SClientset(options...)
			if err != nil {
				t.Fatalf("create clientset: %v", err)
			}
			template := &resourcev1.ResourceClaimTemplate{
				ObjectMeta: metav1.ObjectMeta{Name: "template", Labels: tc.labels},
				Spec: resourcev1.ResourceClaimTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{Labels: tc.labels},
				},
			}
			// Keep the metadata maps independent so labeling one cannot label both.
			template = template.DeepCopy()

			templates := cs.ResourceV1().ResourceClaimTemplates("test")
			created, err := templates.Create(t.Context(), template, metav1.CreateOptions{})
			if err != nil {
				t.Fatalf("create resource claim template: %v", err)
			}
			if diff := cmp.Diff(tc.want, created.Labels); diff != "" {
				t.Errorf("created metadata labels mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.want, created.Spec.Labels); diff != "" {
				t.Errorf("created spec metadata labels mismatch (-want +got):\n%s", diff)
			}

			stored, err := templates.Get(t.Context(), "template", metav1.GetOptions{})
			if err != nil {
				t.Fatalf("read resource claim template: %v", err)
			}

			if diff := cmp.Diff(tc.want, stored.Labels); diff != "" {
				t.Errorf("stored metadata labels mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.want, stored.Spec.Labels); diff != "" {
				t.Errorf("stored spec metadata labels mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
