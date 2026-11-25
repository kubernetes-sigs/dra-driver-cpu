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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// DeleteNamespacedObjects deletes one pass of labeled objects from a namespace.
// It returns true when a pass finds no labeled objects to delete.
func DeleteNamespacedObjects(ctx context.Context, cs kubernetes.Interface, namespace string) (bool, error) {
	objRefs, err := ListNamespacedObjects(ctx, cs, namespace)
	if err != nil {
		return false, fmt.Errorf("failed to list namespace objects: %w", err)
	}
	// indicates that all objects are gone
	if len(objRefs) == 0 {
		return true, nil
	}
	for _, ref := range objRefs {
		uid := ref.UID
		options := metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}
		if err := deleteObjectRef(ctx, cs, ref, options); err != nil && !apierrors.IsNotFound(err) && !apierrors.IsConflict(err) {
			return false, fmt.Errorf("delete %s %s/%s: %w", ref.GVR, ref.Namespace, ref.Name, err)
		}
	}
	return false, nil
}

func deleteObjectRef(ctx context.Context, cs kubernetes.Interface, ref ObjectRef, options metav1.DeleteOptions) error {
	switch ref.GVR {
	case corev1.SchemeGroupVersion.WithResource("pods"):
		return cs.CoreV1().Pods(ref.Namespace).Delete(ctx, ref.Name, options)
	case resourcev1.SchemeGroupVersion.WithResource("resourceclaims"):
		return cs.ResourceV1().ResourceClaims(ref.Namespace).Delete(ctx, ref.Name, options)
	case resourcev1.SchemeGroupVersion.WithResource("resourceclaimtemplates"):
		return cs.ResourceV1().ResourceClaimTemplates(ref.Namespace).Delete(ctx, ref.Name, options)
	default:
		return fmt.Errorf("cannot delete unsupported resource %s", ref.GVR)
	}
}
