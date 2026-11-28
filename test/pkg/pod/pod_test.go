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

package pod_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	e2epod "github.com/kubernetes-sigs/dra-driver-cpu/test/pkg/pod"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestWaitToBeRunning(t *testing.T) {
	const (
		namespace = "test-ns"
		podName   = "multi-container-pod"
	)

	makePod := func(phase v1.PodPhase, statuses []v1.ContainerStatus) *v1.Pod {
		return &v1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: namespace,
				Name:      podName,
			},
			Spec: v1.PodSpec{
				Containers: []v1.Container{
					{Name: "c0"},
					{Name: "c1"},
				},
			},
			Status: v1.PodStatus{
				Phase:             phase,
				ContainerStatuses: statuses,
			},
		}
	}

	t.Run("waits until all containers are running", func(t *testing.T) {
		cs := fake.NewSimpleClientset(makePod(v1.PodRunning, []v1.ContainerStatus{
			{Name: "c0", State: v1.ContainerState{Running: &v1.ContainerStateRunning{}}},
			{Name: "c1", State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "ContainerCreating"}}},
		}))

		var calls atomic.Int32
		cs.PrependReactor("get", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
			if calls.Add(1) == 1 {
				return false, nil, nil
			}
			return true, makePod(v1.PodRunning, []v1.ContainerStatus{
				{Name: "c0", State: v1.ContainerState{Running: &v1.ContainerStateRunning{}}},
				{Name: "c1", State: v1.ContainerState{Running: &v1.ContainerStateRunning{}}},
			}), nil
		})

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		if err := e2epod.WaitToBeRunning(ctx, cs, namespace, podName); err != nil {
			t.Fatalf("WaitToBeRunning() unexpected error: %v", err)
		}
		if got := calls.Load(); got < 2 {
			t.Fatalf("expected WaitToBeRunning to poll until c1 is running, got %d poll(s)", got)
		}
	})

	t.Run("fails when pod phase is Running but a container stays waiting", func(t *testing.T) {
		cs := fake.NewSimpleClientset(makePod(v1.PodRunning, []v1.ContainerStatus{
			{Name: "c0", State: v1.ContainerState{Running: &v1.ContainerStateRunning{}}},
			{Name: "c1", State: v1.ContainerState{Waiting: &v1.ContainerStateWaiting{Reason: "CreateContainerError"}}},
		}))

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		err := e2epod.WaitToBeRunning(ctx, cs, namespace, podName)
		if err == nil {
			t.Fatal("WaitToBeRunning() succeeded while c1 was still waiting")
		}
		if !strings.Contains(err.Error(), "c1") {
			t.Fatalf("WaitToBeRunning() error = %q, want non-running container name c1", err)
		}
	})
}
