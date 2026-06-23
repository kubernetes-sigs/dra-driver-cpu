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

package pod

import (
	"errors"

	"github.com/go-logr/logr"
	"github.com/onsi/gomega/gcustom"
	"github.com/onsi/gomega/types"
	v1 "k8s.io/api/core/v1"
)

const ReasonCreateContainerError = "CreateContainerError"

// BeFailedToCreate succeeds if the pod is Pending with a container in
// CreateContainerError waiting state.
func BeFailedToCreate(logr logr.Logger) types.GomegaMatcher {
	return gcustom.MakeMatcher(func(actual *v1.Pod) (bool, error) {
		if actual == nil {
			return false, errors.New("nil Pod")
		}
		logger := logr.WithValues("podUID", actual.UID, "namespace", actual.Namespace, "name", actual.Name)
		if actual.Status.Phase != v1.PodPending {
			logger.Info("unexpected phase", "phase", actual.Status.Phase)
			return false, nil
		}
		cntSt := findWaitingContainerStatus(actual.Status.ContainerStatuses)
		if cntSt == nil {
			logger.Info("no container in waiting state")
			return false, nil
		}
		if cntSt.State.Waiting.Reason != ReasonCreateContainerError {
			logger.Info("container waiting for different reason", "containerName", cntSt.Name, "reason", cntSt.State.Waiting.Reason)
			return false, nil
		}
		logger.Info("container creation error", "containerName", cntSt.Name)
		return true, nil
	}).WithTemplate("Pod {{.Actual.Namespace}}/{{.Actual.Name}} UID {{.Actual.UID}} was not in failed phase")
}

func findWaitingContainerStatus(statuses []v1.ContainerStatus) *v1.ContainerStatus {
	for idx := range statuses {
		cntSt := &statuses[idx]
		if cntSt.State.Waiting != nil {
			return cntSt
		}
	}
	return nil
}
