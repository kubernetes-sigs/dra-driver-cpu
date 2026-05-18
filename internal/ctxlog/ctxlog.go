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

package ctxlog

import (
	"context"
	"flag"
	"log"
	"runtime/debug"

	"github.com/go-logr/logr"
	"github.com/go-logr/stdr"
	"k8s.io/klog/v2"
	"k8s.io/klog/v2/textlogger"
)

var (
	fallback = stdr.New(log.Default())
	cfg      = textlogger.NewConfig()
	logger   logr.Logger
	inited   bool
)

// AddFlags registers the logging flags on the given flag set.
func AddFlags(fs *flag.FlagSet) {
	cfg.AddFlags(fs)
}

func Setup() logr.Logger {
	if inited {
		logger.Info("bug: Setup() called more than once", "stack", string(debug.Stack()))
		return logger
	}
	logger = textlogger.NewLogger(cfg)
	// some key deps still call klog directly, so we need this integration.
	// TODO: check every time we bump kube libs to a new major version,
	// as the contextual logging transition to kube libs is still ongoing
	// k8s.io/client-go
	// k8s.io/apimachinery
	// k8s.io/dynamic-resource-allocation
	// k8s.io/kube-openapi
	// k8s.io/utils
	// k8s.io/component-helpers
	// k8s.io/kubelet
	klog.SetLoggerWithOptions(logger, klog.ContextualLogger(true))
	inited = true
	return logger
}

func Flush() {
	klog.Flush()
}

// NewContext returns a new context derived from ctx that carries the provided logger.
func NewContext(ctx context.Context, logger logr.Logger) context.Context {
	return logr.NewContext(ctx, logger)
}

// FromContext returns the logger stored in ctx. If no logger is found,
// it falls back to a stdlib-based logger via stdr rather than discarding
// messages silently.
// TODO: pending item: enriched context loss
func FromContext(ctx context.Context) logr.Logger {
	logger, err := logr.FromContext(ctx)
	if err != nil {
		return fallback
	}
	return logger
}

// KMetadata is satisfied by Kubernetes objects and NRI protobuf types
// that expose name and namespace.
type KMetadata interface {
	GetName() string
	GetNamespace() string
}

// ObjectRef references a kubernetes object.
type ObjectRef struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace,omitempty"`
}

func (ref ObjectRef) String() string {
	if ref.Namespace != "" {
		return ref.Namespace + "/" + ref.Name
	}
	return ref.Name
}

// MarshalLog implements logr.Marshaler.
func (ref ObjectRef) MarshalLog() interface{} {
	return ref
}

// KObj returns an ObjectRef for the given object, for use as a log value.
func KObj(obj KMetadata) ObjectRef {
	return ObjectRef{
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
	}
}
