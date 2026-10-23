/*
Copyright 2025 The Kubernetes Authors.

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
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/client"
	"github.com/onsi/ginkgo/v2"
	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

func By(format string, args ...any) {
	ginkgo.By(fmt.Sprintf(format, args...))
}

type Fixture struct {
	Prefix       string
	K8SClientset kubernetes.Interface
	Namespace    *v1.Namespace
	Log          logr.Logger
	// skipNamespaceTeardown is set when the namespace was provided externally
	// and should not be deleted on teardown. Resources within it are still
	// cleaned up so specs do not get polluted by each other.
	skipNamespaceTeardown bool
}

func ForGinkgo() (*Fixture, error) {
	cs, err := client.NewK8SClientset()
	if err != nil {
		return nil, err
	}
	return &Fixture{
		K8SClientset: cs,
		Log:          ginkgo.GinkgoLogr,
	}, nil
}

func (fxt *Fixture) WithPrefix(prefix string) *Fixture {
	return &Fixture{
		Prefix:       prefix,
		K8SClientset: fxt.K8SClientset,
		Log:          fxt.Log,
	}
}

func (fxt *Fixture) Setup(ctx context.Context) error {
	if fxt.Namespace != nil {
		return nil // TODO: or fail?
	}
	ns, exists, err := checkExistingNamespace(ctx, fxt.K8SClientset)
	if err != nil {
		return fmt.Errorf("failed to check existing namespace: %w", err)
	}
	if exists {
		fxt.Namespace = ns
		fxt.skipNamespaceTeardown = true
		fxt.Log.Info("fixture setup skipped (existing namespace provided externally - won't be deleted on teardown)", "namespace", ns.Name)
		return nil
	}
	if ns == nil {
		generateName := "dracpu-e2e-"
		if fxt.Prefix != "" {
			generateName += fxt.Prefix + "-"
		}
		ns = &v1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: generateName,
			},
		}
	}
	nsCreated, err := fxt.K8SClientset.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create namespace %s: %w", ns.Name, err)
	}
	fxt.Namespace = nsCreated
	fxt.Log.Info("fixture setup", "namespace", fxt.Namespace.Name)
	return nil
}

func (fxt *Fixture) Teardown(ctx context.Context) error {
	if fxt.Namespace == nil {
		return nil // TODO: or fail?
	}
	if fxt.skipNamespaceTeardown {
		fxt.Log.Info("fixture teardown: cleaning resources in externally provided namespace", "namespace", fxt.Namespace.Name)
		if err := fxt.cleanNamespaceResources(ctx); err != nil {
			return fmt.Errorf("failed to clean resources in namespace %s: %w", fxt.Namespace.Name, err)
		}
		fxt.Namespace = nil
		return nil
	}
	err := fxt.K8SClientset.CoreV1().Namespaces().Delete(ctx, fxt.Namespace.Name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("failed to delete namespace %s: %w", fxt.Namespace.Name, err)
	}
	err = waitForNamespaceToBeDeleted(ctx, fxt.K8SClientset, fxt.Namespace.Name)
	if err != nil {
		return err
	}
	fxt.Log.Info("fixture teardown", "namespace", fxt.Namespace.Name)
	fxt.Namespace = nil
	return nil
}

// cleanNamespaceResources deletes all pods, resource claims, and resource claim
// templates in the namespace and waits for the pods to be fully removed. This
// releases exclusive CPU allocations so subsequent specs start with a clean
// slate, without requiring the namespace itself to be deleted.
func (fxt *Fixture) cleanNamespaceResources(ctx context.Context) error {
	nsName := fxt.Namespace.Name
	cs := fxt.K8SClientset

	fxt.Log.Info("deleting all pods", "namespace", nsName)
	if err := cs.CoreV1().Pods(nsName).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete pods: %w", err)
	}

	fxt.Log.Info("waiting for all pods to be deleted", "namespace", nsName)
	if err := waitForAllPodsDeleted(ctx, cs, nsName); err != nil {
		return err
	}

	fxt.Log.Info("deleting all resource claim templates", "namespace", nsName)
	if err := cs.ResourceV1().ResourceClaimTemplates(nsName).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete resource claim templates: %w", err)
	}

	fxt.Log.Info("deleting all resource claims", "namespace", nsName)
	if err := cs.ResourceV1().ResourceClaims(nsName).DeleteCollection(ctx, metav1.DeleteOptions{}, metav1.ListOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete resource claims: %w", err)
	}

	fxt.Log.Info("waiting for all resource claims to be deleted", "namespace", nsName)
	if err := waitForAllResourceClaimsDeleted(ctx, cs, nsName); err != nil {
		return err
	}

	fxt.Log.Info("fixture teardown: namespace resources cleaned", "namespace", nsName)
	return nil
}

const (
	nsPollInterval = time.Second
	nsPollTimeout  = time.Minute * 2
)

func waitForNamespaceToBeDeleted(ctx context.Context, cs kubernetes.Interface, nsName string) error {
	immediate := true
	err := wait.PollUntilContextTimeout(ctx, nsPollInterval, nsPollTimeout, immediate, func(ctx2 context.Context) (done bool, err error) {
		_, err = cs.CoreV1().Namespaces().Get(ctx2, nsName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("namespace=%s was not deleted: %w", nsName, err)
	}
	return nil
}

func waitForAllPodsDeleted(ctx context.Context, cs kubernetes.Interface, nsName string) error {
	immediate := true
	return wait.PollUntilContextTimeout(ctx, nsPollInterval, nsPollTimeout, immediate, func(ctx2 context.Context) (done bool, err error) {
		pods, err := cs.CoreV1().Pods(nsName).List(ctx2, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		return len(pods.Items) == 0, nil
	})
}

func waitForAllResourceClaimsDeleted(ctx context.Context, cs kubernetes.Interface, nsName string) error {
	immediate := true
	return wait.PollUntilContextTimeout(ctx, nsPollInterval, nsPollTimeout, immediate, func(ctx2 context.Context) (done bool, err error) {
		claims, err := cs.ResourceV1().ResourceClaims(nsName).List(ctx2, metav1.ListOptions{})
		if err != nil {
			return false, err
		}
		return len(claims.Items) == 0, nil
	})
}

// checkExistingNamespace checks if the namespace name provided externally and returns its name and a boolean indicating if it already exists.
func checkExistingNamespace(ctx context.Context, cs kubernetes.Interface) (*v1.Namespace, bool, error) {
	nsName := os.Getenv("DRACPU_E2E_FIXTURE_NAMESPACE")
	if nsName == "" {
		return nil, false, nil
	}

	ns, err := cs.CoreV1().Namespaces().Get(ctx, nsName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return &v1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: nsName}}, false, nil
		}
		return &v1.Namespace{}, false, err
	}

	return ns, true, nil
}
