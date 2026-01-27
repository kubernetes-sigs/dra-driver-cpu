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

package pod

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	v1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

const (
	PollInterval = time.Second * 10
	PollTimeout  = time.Minute * 2
)

func CreateSync(ctx context.Context, cs kubernetes.Interface, pod *v1.Pod) (*v1.Pod, error) {
	createdPod, err := cs.CoreV1().Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("error creating pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}
	if err = WaitToBeRunning(ctx, cs, createdPod.Namespace, createdPod.Name); err != nil {
		return nil, err
	}

	// Get the newest pod after it becomes running and ready, some status may change after pod created, such as pod ip.
	return cs.CoreV1().Pods(createdPod.Namespace).Get(ctx, createdPod.Name, metav1.GetOptions{})
}

func DeleteSync(ctx context.Context, cs kubernetes.Interface, pod *v1.Pod) error {
	err := cs.CoreV1().Pods(pod.Namespace).Delete(ctx, pod.Name, metav1.DeleteOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil // Already deleted
		}
		return fmt.Errorf("error deleting pod %s/%s: %w", pod.Namespace, pod.Name, err)
	}
	return WaitToBeDeleted(ctx, cs, pod.Namespace, pod.Name)
}

func RunToCompletion(ctx context.Context, cs kubernetes.Interface, pod *v1.Pod) (*v1.Pod, error) {
	createdPod, err := cs.CoreV1().Pods(pod.Namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("error creating pod %s/%s: %w", createdPod.Namespace, createdPod.Name, err)
	}
	_, err = WaitForPhase(ctx, cs, createdPod.Namespace, createdPod.Name, v1.PodSucceeded)
	if err != nil {
		return nil, err
	}
	// Get the newest pod after it becomes running and ready, some status may change after pod created, such as pod ip.
	return cs.CoreV1().Pods(createdPod.Namespace).Get(ctx, createdPod.Name, metav1.GetOptions{})
}

func WaitToBeRunning(ctx context.Context, cs kubernetes.Interface, podNamespace, podName string) error {
	phase, err := WaitForPhase(ctx, cs, podNamespace, podName, v1.PodRunning)
	if err != nil {
		return fmt.Errorf("pod=%s/%s is not Running; phase=%q; %w", podNamespace, podName, phase, err)
	}
	return nil
}

func WaitToBeDeleted(ctx context.Context, cs kubernetes.Interface, podNamespace, podName string) error {
	immediate := true
	err := wait.PollUntilContextTimeout(ctx, PollInterval, PollTimeout, immediate, func(ctx2 context.Context) (done bool, err error) {
		_, err = cs.CoreV1().Pods(podNamespace).Get(ctx2, podName, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				return true, nil
			}
			return false, err
		}
		return false, nil
	})
	if err != nil {
		return fmt.Errorf("pod=%s/%s was not deleted: %w", podNamespace, podName, err)
	}
	return nil
}

func WaitForPhase(ctx context.Context, cs kubernetes.Interface, podNamespace, podName string, desiredPhase v1.PodPhase) (v1.PodPhase, error) {
	immediate := true
	podPhase := v1.PodUnknown
	err := wait.PollUntilContextTimeout(ctx, PollInterval, PollTimeout, immediate, func(ctx2 context.Context) (done bool, err error) {
		pod, err := cs.CoreV1().Pods(podNamespace).Get(ctx2, podName, metav1.GetOptions{})
		if err != nil {
			return false, err
		}

		podPhase = pod.Status.Phase
		if podPhase == desiredPhase {
			return true, nil
		}
		return false, nil
	})
	return podPhase, err
}

func GetLogs(cs kubernetes.Interface, ctx context.Context, podNamespace, podName, containerName string) (string, error) {
	previous := false
	request := cs.CoreV1().RESTClient().Get().Resource("pods").Namespace(podNamespace).Name(podName).SubResource("log").Param("container", containerName).Param("previous", strconv.FormatBool(previous))
	logs, err := request.Do(ctx).Raw()
	if err != nil {
		return "", err
	}
	if strings.Contains(string(logs), "Internal Error") {
		return "", fmt.Errorf("fetched log contains \"Internal Error\": %q", string(logs))
	}
	return string(logs), err
}

func PinToNode(pod *v1.Pod, nodeName string) *v1.Pod {
	pod.Spec.NodeSelector = map[string]string{
		"kubernetes.io/hostname": nodeName,
	}
	return pod
}

func Identify(pod *v1.Pod) string {
	return pod.Namespace + "/" + pod.Name + "@" + pod.Spec.NodeName
}
