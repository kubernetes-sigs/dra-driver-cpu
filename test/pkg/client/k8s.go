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

package client

import (
	"errors"
	"fmt"
	"os"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
)

func NewK8SClientset() (kubernetes.Interface, error) {
	kubeconfig, ok := os.LookupEnv("KUBECONFIG")
	if !ok {
		return nil, errors.New("missing environment variable KUBECONFIG")
	}
	config, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	if err != nil {
		return nil, fmt.Errorf("can not create client-go configuration: %w", err)
	}
	// Avoid "rate: Wait(n=1) would exceed context deadline" during e2e polling.
	config.QPS = 50
	config.Burst = 100

	return kubernetes.NewForConfig(config)
}
