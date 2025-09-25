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
	generateName := "dracpu-e2e-"
	if fxt.Prefix != "" {
		generateName += fxt.Prefix + "-"
	}
	ns := &v1.Namespace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: generateName,
		},
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

const (
	nsPollInterval = time.Second * 10
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
