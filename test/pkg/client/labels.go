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

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	resourcev1client "k8s.io/client-go/kubernetes/typed/resource/v1"
)

type registeredResource struct {
	gvr  schema.GroupVersionResource
	list func(context.Context, kubernetes.Interface, string, metav1.ListOptions) (runtime.Object, error)
}

type resourceLister[T runtime.Object] interface {
	List(context.Context, metav1.ListOptions) (T, error)
}

type resourceRegistration[T runtime.Object, C resourceLister[T]] struct {
	registeredResource
	client func(kubernetes.Interface, string) C
}

func registerResource[T runtime.Object, C resourceLister[T]](gvr schema.GroupVersionResource, client func(kubernetes.Interface, string) C) resourceRegistration[T, C] {
	return resourceRegistration[T, C]{
		registeredResource: registeredResource{
			gvr: gvr,
			list: func(ctx context.Context, cs kubernetes.Interface, namespace string, opts metav1.ListOptions) (runtime.Object, error) {
				return client(cs, namespace).List(ctx, opts)
			},
		},
		client: client,
	}
}

var (
	wrappedPods = registerResource[*corev1.PodList](corev1.SchemeGroupVersion.WithResource("pods"),
		func(cs kubernetes.Interface, namespace string) corev1client.PodInterface {
			return cs.CoreV1().Pods(namespace)
		})
	wrappedResourceClaims = registerResource[*resourcev1.ResourceClaimList](resourcev1.SchemeGroupVersion.WithResource("resourceclaims"),
		func(cs kubernetes.Interface, namespace string) resourcev1client.ResourceClaimInterface {
			return cs.ResourceV1().ResourceClaims(namespace)
		})
	wrappedResourceClaimTemplates = registerResource[*resourcev1.ResourceClaimTemplateList](resourcev1.SchemeGroupVersion.WithResource("resourceclaimtemplates"),
		func(cs kubernetes.Interface, namespace string) resourcev1client.ResourceClaimTemplateInterface {
			return cs.ResourceV1().ResourceClaimTemplates(namespace)
		})
	wrappedResources = []registeredResource{
		wrappedPods.registeredResource,
		wrappedResourceClaims.registeredResource,
		wrappedResourceClaimTemplates.registeredResource,
	}
)

// labelingClientset adds the test ownership label to the objects the e2e suite
// creates. It wraps only the clients that the suite uses; all other calls pass
// through to the embedded clientset.
type labelingClientset struct {
	kubernetes.Interface
	key   string
	value string
}

func (c *labelingClientset) CoreV1() corev1client.CoreV1Interface {
	return &labelingCoreV1{
		CoreV1Interface: c.Interface.CoreV1(),
		owner:           c,
	}
}

func (c *labelingClientset) ResourceV1() resourcev1client.ResourceV1Interface {
	return &labelingResourceV1{
		ResourceV1Interface: c.Interface.ResourceV1(),
		owner:               c,
	}
}

type labelingCoreV1 struct {
	corev1client.CoreV1Interface
	owner *labelingClientset
}

func (c *labelingCoreV1) Pods(namespace string) corev1client.PodInterface {
	return &labelingPods{
		PodInterface: wrappedPods.client(c.owner.Interface, namespace),
		owner:        c.owner,
	}
}

func (c *labelingCoreV1) Namespaces() corev1client.NamespaceInterface {
	return &labelingNamespaces{
		NamespaceInterface: c.CoreV1Interface.Namespaces(),
		owner:              c.owner,
	}
}

type labelingPods struct {
	corev1client.PodInterface
	owner *labelingClientset
}

func (c *labelingPods) Create(ctx context.Context, pod *corev1.Pod, opts metav1.CreateOptions) (*corev1.Pod, error) {
	addCreateLabel(pod, c.owner.key, c.owner.value)
	return c.PodInterface.Create(ctx, pod, opts)
}

type labelingNamespaces struct {
	corev1client.NamespaceInterface
	owner *labelingClientset
}

func (c *labelingNamespaces) Create(ctx context.Context, namespace *corev1.Namespace, opts metav1.CreateOptions) (*corev1.Namespace, error) {
	addCreateLabel(namespace, c.owner.key, c.owner.value)
	return c.NamespaceInterface.Create(ctx, namespace, opts)
}

type labelingResourceV1 struct {
	resourcev1client.ResourceV1Interface
	owner *labelingClientset
}

func (c *labelingResourceV1) ResourceClaims(namespace string) resourcev1client.ResourceClaimInterface {
	return &labelingResourceClaims{
		ResourceClaimInterface: wrappedResourceClaims.client(c.owner.Interface, namespace),
		owner:                  c.owner,
	}
}

func (c *labelingResourceV1) ResourceClaimTemplates(namespace string) resourcev1client.ResourceClaimTemplateInterface {
	return &labelingResourceClaimTemplates{
		ResourceClaimTemplateInterface: wrappedResourceClaimTemplates.client(c.owner.Interface, namespace),
		owner:                          c.owner,
	}
}

type labelingResourceClaims struct {
	resourcev1client.ResourceClaimInterface
	owner *labelingClientset
}

func (c *labelingResourceClaims) Create(ctx context.Context, claim *resourcev1.ResourceClaim, opts metav1.CreateOptions) (*resourcev1.ResourceClaim, error) {
	addCreateLabel(claim, c.owner.key, c.owner.value)
	return c.ResourceClaimInterface.Create(ctx, claim, opts)
}

type labelingResourceClaimTemplates struct {
	resourcev1client.ResourceClaimTemplateInterface
	owner *labelingClientset
}

func (c *labelingResourceClaimTemplates) Create(ctx context.Context, template *resourcev1.ResourceClaimTemplate, opts metav1.CreateOptions) (*resourcev1.ResourceClaimTemplate, error) {
	addCreateLabel(template, c.owner.key, c.owner.value)
	// Generated claims inherit spec metadata, not the template's own labels.
	addCreateLabel(&template.Spec.ObjectMeta, c.owner.key, c.owner.value)
	return c.ResourceClaimTemplateInterface.Create(ctx, template, opts)
}

func addCreateLabel(obj metav1.Object, key, value string) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[key] = value
	obj.SetLabels(labels)
}
