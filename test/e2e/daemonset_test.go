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

package e2e

import (
	"context"
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	daemonSetNamespace    = "kube-system"
	daemonSetLabel        = "app=dracpu"
	driverPodPollInterval = 2 * time.Second
	driverPodPollTimeout  = 2 * time.Minute
)

func listDriverPods(ctx context.Context, client kubernetes.Interface) ([]corev1.Pod, error) {
	podList, err := client.CoreV1().Pods(daemonSetNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: daemonSetLabel,
	})
	if err != nil {
		return nil, fmt.Errorf("listing pods with selector %q in %q: %w",
			daemonSetLabel, daemonSetNamespace, err)
	}
	return podList.Items, nil
}

func waitForRunningDriverPods(ctx context.Context, client kubernetes.Interface) []corev1.Pod {
	ginkgo.GinkgoHelper()

	var pods []corev1.Pod
	gomega.Eventually(func(g gomega.Gomega) {
		var err error
		pods, err = listDriverPods(ctx, client)
		g.Expect(err).NotTo(gomega.HaveOccurred())
		g.Expect(pods).NotTo(gomega.BeEmpty(),
			"no dra-driver-cpu pods found with selector %q in namespace %q",
			daemonSetLabel, daemonSetNamespace)
		for _, pod := range pods {
			g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodRunning),
				"pod %q on node %q is not Running (phase=%s)",
				pod.Name, pod.Spec.NodeName, pod.Status.Phase)
		}
	}, driverPodPollTimeout, driverPodPollInterval).Should(gomega.Succeed(),
		"timed out waiting for dra-driver-cpu pods to reach Running phase")

	return pods
}
