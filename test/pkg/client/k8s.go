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
	"errors"
	"fmt"
	"net/http"
	"os"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// TestResourceLabelKey and TestResourceLabelValue mark objects created by the e2e suite.
	// The fixture can use the pair to find objects that it owns, for example to clean up
	// a namespace that the suite did not create.
	TestResourceLabelKey   = "app.kubernetes.io/managed-by"
	TestResourceLabelValue = "dra-driver-cpu-e2e"
)

func NewK8SConfig() (*rest.Config, error) {
	kubeconfig, ok := os.LookupEnv("KUBECONFIG")
	if !ok {
		return nil, errors.New("missing environment variable KUBECONFIG")
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("can not create client-go configuration: %w", err)
	}
	return config, nil
}

// Option configures the clientset returned by NewK8SClientset.
type Option func(*clientOptions)

type clientOptions struct {
	injectCreateLabels bool
	baseClientset      kubernetes.Interface
}

func defaultClientOptions() clientOptions {
	return clientOptions{
		injectCreateLabels: true,
	}
}

// WithoutCreateLabelInjection disables adding TestResourceLabelKey to objects
// created through the clientset.
func WithoutCreateLabelInjection() Option {
	return func(opts *clientOptions) {
		opts.injectCreateLabels = false
	}
}

// WithClientset uses clientset instead of creating one from KUBECONFIG.
// It is intended for tests.
func WithClientset(clientset kubernetes.Interface) Option {
	return func(opts *clientOptions) {
		opts.baseClientset = clientset
	}
}

func NewK8SClientset(options ...Option) (kubernetes.Interface, error) {
	opts := defaultClientOptions()
	for _, option := range options {
		option(&opts)
	}

	clientset := opts.baseClientset
	if clientset == nil {
		config, err := NewK8SConfig()
		if err != nil {
			return nil, err
		}
		if opts.injectCreateLabels {
			// The audit reads JSON bodies; generated clients default to protobuf.
			config.ContentType = runtime.ContentTypeJSON
			config.AcceptContentTypes = runtime.ContentTypeJSON
			config.Wrap(func(rt http.RoundTripper) http.RoundTripper {
				return &createLabelAudit{
					base:  rt,
					key:   TestResourceLabelKey,
					value: TestResourceLabelValue,
				}
			})
		}
		clientset, err = kubernetes.NewForConfig(config)
		if err != nil {
			return nil, err
		}
	}
	if !opts.injectCreateLabels {
		return clientset, nil
	}
	return &labelingClientset{
		Interface: clientset,
		key:       TestResourceLabelKey,
		value:     TestResourceLabelValue,
	}, nil
}
