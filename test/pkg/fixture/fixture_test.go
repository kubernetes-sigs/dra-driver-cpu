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

package fixture

import (
	"context"
	"testing"
	"time"

	"github.com/go-logr/logr"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const testNamespace = "dracpu-e2e-test"

// newClientset names created namespaces like the apiserver would for GenerateName.
func newClientset(objects ...runtime.Object) *fake.Clientset {
	cs := fake.NewClientset(objects...)
	cs.PrependReactor("create", "namespaces", func(action k8stesting.Action) (bool, runtime.Object, error) {
		ns := action.(k8stesting.CreateAction).GetObject().(*v1.Namespace)
		ns.Name = testNamespace
		return false, nil, nil
	})
	return cs
}

func TestSetupWaitsForDefaultServiceAccount(t *testing.T) {
	sa := &v1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "default", Namespace: testNamespace}}
	fxt := &Fixture{K8SClientset: newClientset(sa), Log: logr.Discard()}
	if err := fxt.Setup(t.Context()); err != nil {
		t.Fatalf("Setup: %v", err)
	}
	if fxt.Namespace == nil || fxt.Namespace.Name != testNamespace {
		t.Fatalf("Namespace = %v, want %s", fxt.Namespace, testNamespace)
	}
}

func TestSetupDeletesNamespaceWhenServiceAccountWaitFails(t *testing.T) {
	cs := newClientset()
	fxt := &Fixture{K8SClientset: cs, Log: logr.Discard()}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	if err := fxt.Setup(ctx); err == nil {
		t.Fatal("Setup succeeded without a default ServiceAccount")
	}
	if fxt.Namespace != nil {
		t.Fatalf("Namespace = %s after failed Setup, want nil so a retry waits again", fxt.Namespace.Name)
	}
	nss, err := cs.CoreV1().Namespaces().List(t.Context(), metav1.ListOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(nss.Items) != 0 {
		t.Fatalf("namespace %s left behind after failed Setup", nss.Items[0].Name)
	}
}
