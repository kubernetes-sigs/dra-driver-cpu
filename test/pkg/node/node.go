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
	"math/rand/v2"
	"os"
	"time"

	"github.com/go-logr/logr"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	k8swait "k8s.io/apimachinery/pkg/util/wait"
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

func IsReady(node *v1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == v1.NodeReady {
			return cond.Status == v1.ConditionTrue
		}
	}
	return false
}

// PickWorker returns a worker Node object to work with; if the environment variable DRACPU_E2E_TARGET_NODE is set,
// fetches the node with that name or fails; otherwise pick a random worker node.
// The random selection algorithm is an implementation detail, and is not guaranteed to be crypto strong.
func PickWorker(ctx context.Context, cs kubernetes.Interface, interval, timeout time.Duration, logger logr.Logger) (*v1.Node, error) {
	// explicit user choice
	if targetNodeName := os.Getenv("DRACPU_E2E_TARGET_NODE"); len(targetNodeName) > 0 {
		return cs.CoreV1().Nodes().Get(ctx, targetNodeName, metav1.GetOptions{})
	}

	// random pick
	var node *v1.Node
	immediate := false // shortcut to make the call more readable
	err := k8swait.PollUntilContextTimeout(ctx, interval, timeout, immediate, func(ctx context.Context) (bool, error) {
		workerNodes, err := FindWorkers(ctx, cs)
		if err != nil {
			// in CI, especially using kind clusters, it may happen that worker nodes are slow to be
			// added to the cluster. Let's not give up immediately.
			logger.Info("failed to find worker nodes, will retry", "error", err)
			return false, nil
		}
		if len(workerNodes) == 0 {
			// may happen if no nodes are ready yet, which is especially true in CI
			return false, nil
		}
		node = workerNodes[rand.IntN(len(workerNodes))] // nolint:gosec
		return true, nil
	})
	return node, err
}
