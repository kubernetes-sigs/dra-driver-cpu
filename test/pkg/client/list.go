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
	"errors"
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
)

// ObjectRef identifies an object and its incarnation so callers can use a UID
// precondition when deleting it.
type ObjectRef struct {
	GVR       schema.GroupVersionResource
	Namespace string
	Name      string
	UID       types.UID
}

func ListNamespacedObjects(ctx context.Context, cs kubernetes.Interface, namespace string) ([]ObjectRef, error) {
	return ListNamespacedObjectsWithLabels(ctx, cs, namespace, map[string]string{TestResourceLabelKey: TestResourceLabelValue})
}

func ListNamespacedObjectsWithLabels(ctx context.Context, cs kubernetes.Interface, namespace string, matchLabels map[string]string) ([]ObjectRef, error) {
	if namespace == "" {
		return nil, errors.New("missing namespace: provide a nonempty namespace to avoid listing all namespaces")
	}
	if len(matchLabels) == 0 {
		return nil, errors.New("missing ownership labels: provide at least one label key/value pair")
	}
	selector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{MatchLabels: matchLabels})
	if err != nil {
		return nil, fmt.Errorf("invalid ownership labels: provide valid label keys and values: %w", err)
	}

	var refs []ObjectRef
	for _, resource := range wrappedResources {
		opts := metav1.ListOptions{LabelSelector: selector.String()}
		list, err := resource.list(ctx, cs, namespace, opts)
		if err != nil {
			return nil, fmt.Errorf("list %s in namespace %q: %w", resource.gvr, namespace, err)
		}
		items, err := meta.ExtractList(list)
		if err != nil {
			return nil, fmt.Errorf("read %s list in namespace %q: %w", resource.gvr, namespace, err)
		}
		for _, item := range items {
			obj, err := meta.Accessor(item)
			if err != nil {
				return nil, fmt.Errorf("read %s object metadata in namespace %q: %w", resource.gvr, namespace, err)
			}
			refs = append(refs, ObjectRef{
				GVR:       resource.gvr,
				Namespace: obj.GetNamespace(),
				Name:      obj.GetName(),
				UID:       obj.GetUID(),
			})
		}
	}
	return refs, nil
}
