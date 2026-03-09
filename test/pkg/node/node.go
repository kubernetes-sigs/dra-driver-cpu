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

package node

import (
	"context"
	"errors"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

func FindWorkers(ctx context.Context, cs kubernetes.Interface) ([]*v1.Node, error) {
	selector := labels.Set{"node-role.kubernetes.io/worker": ""}.AsSelector()
	nodeList, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{LabelSelector: selector.String()})
	if err != nil {
		return nil, err
	}

	var workerNodes []*v1.Node
	for _, n := range nodeList.Items {
		if !IsReady(&n) {
			continue
		}
		workerNodes = append(workerNodes, &n)
	}
	return workerNodes, nil
}

// FindWorkersOrAnyReady returns nodes with node-role.kubernetes.io/worker that are ready.
// If none are found (e.g. kind or clusters that do not use that label), it returns any ready nodes.
// Returns an error if listing fails or there are no ready nodes at all.
func FindWorkersOrAnyReady(ctx context.Context, cs kubernetes.Interface) ([]*v1.Node, error) {
	nodes, err := FindWorkers(ctx, cs)
	if err != nil {
		return nil, err
	}
	if len(nodes) > 0 {
		return nodes, nil
	}
	allNodes, err := cs.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	for i := range allNodes.Items {
		if IsReady(&allNodes.Items[i]) {
			nodes = append(nodes, &allNodes.Items[i])
		}
	}
	if len(nodes) == 0 {
		return nil, errors.New("no ready nodes")
	}
	return nodes, nil
}

func IsReady(node *v1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == v1.NodeReady {
			return cond.Status == v1.ConditionTrue
		}
	}
	return false
}
