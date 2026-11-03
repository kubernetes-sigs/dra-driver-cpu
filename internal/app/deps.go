/*
Copyright 2026 The Kubernetes Authors.

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

package app

import (
	"context"
	"fmt"
	"net"

	"github.com/go-logr/logr"
	"github.com/kubernetes-sigs/dra-driver-cpu/internal/driverconfig"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/driver"
	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/sysfs"
	"github.com/prometheus/client_golang/prometheus"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// Driver is the lifecycle surface the application needs from the DRA driver.
type Driver interface {
	Start(context.Context) (<-chan error, error)
	Stop()
}

// Metrics contains the registry used by the driver and the gatherer used by the
// HTTP endpoint. Keeping them separate supports both the global defaults and an
// isolated registry in tests.
type Metrics struct {
	Registerer prometheus.Registerer
	Gatherer   prometheus.Gatherer
}

// Dependencies contains the process-level dependencies of the application.
type Dependencies struct {
	Listen        func(network, address string) (net.Listener, error)
	NewSysFS      func(logr.Logger, string) (sysfs.FS, error)
	NewKubeClient func(driverconfig.Config) (kubernetes.Interface, error)
	NewNodeName   func(string) (string, error)
	NewDriver     func(logr.Logger, driver.Providers, *driver.Config) (Driver, error)
	Metrics       Metrics
}

// DefaultDependencies returns the production dependency implementations.
func DefaultDependencies() Dependencies {
	return Dependencies{
		Listen:        net.Listen,
		NewSysFS:      newSysFS,
		NewKubeClient: newKubeClient,
		NewNodeName:   driverconfig.ResolveNodeName,
		NewDriver:     newDriver,
		Metrics: Metrics{
			Registerer: prometheus.DefaultRegisterer,
			Gatherer:   prometheus.DefaultGatherer,
		},
	}
}

func newSysFS(logger logr.Logger, overlayPath string) (sysfs.FS, error) {
	sfs, err := sysfs.NewOverlayFromFile(sysfs.Host(), overlayPath)
	if err != nil {
		return nil, err
	}

	if overlayPath != "" {
		logger.Info("loaded sysfs overlay", "path", overlayPath)
	}

	return sfs, nil
}

func newKubeClient(cfg driverconfig.Config) (kubernetes.Interface, error) {
	var restConfig *rest.Config
	var err error
	if cfg.Kubeconfig != "" {
		restConfig, err = clientcmd.BuildConfigFromFlags("", cfg.Kubeconfig)
	} else {
		restConfig, err = rest.InClusterConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("can not create client-go configuration: %w", err)
	}

	// Use protobuf for better performance at scale.
	// https://kubernetes.io/docs/reference/using-api/api-concepts/#alternate-representations-of-resources
	restConfig.AcceptContentTypes = "application/vnd.kubernetes.protobuf,application/json"
	restConfig.ContentType = "application/vnd.kubernetes.protobuf"

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("can not create client-go client: %w", err)
	}
	return clientset, nil
}

func newDriver(logger logr.Logger, providers driver.Providers, cfg *driver.Config) (Driver, error) {
	return driver.New(logger, providers, cfg)
}
