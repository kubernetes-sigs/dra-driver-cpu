module github.com/kubernetes-sigs/dra-driver-cpu

go 1.26.0

// The k8s.io/* staging modules are pinned to master pseudo-versions because the driver uses
// the KEP-5517 DRANodeAllocatableResources alpha2 API (kubernetes/kubernetes#140009, merged
// 2026-07-23), which is not in any tagged release yet. The pseudo-versions were resolved with:
//
//	go get k8s.io/api@master k8s.io/apimachinery@master k8s.io/client-go@master \
//	  k8s.io/component-helpers@master k8s.io/dynamic-resource-allocation@master \
//	  k8s.io/kubelet@master k8s.io/streaming@master
//
// i.e. the current heads of the publishing-bot staging mirrors (github.com/kubernetes/api, ...),
// which sync from kubernetes/kubernetes master. Re-run the same command to bump them together;
// drop back to normal v0.37.x tags once Kubernetes 1.37 is released.
require (
	github.com/containerd/nri v0.11.0
	github.com/go-logr/logr v1.4.3
	github.com/go-logr/stdr v1.2.2
	github.com/google/go-cmp v0.7.0
	github.com/google/jsonschema-go v0.4.3
	github.com/onsi/ginkgo/v2 v2.28.1
	github.com/onsi/gomega v1.40.0
	github.com/prometheus/client_golang v1.24.0
	github.com/prometheus/client_model v0.6.2
	github.com/prometheus/common v0.70.0
	github.com/stretchr/testify v1.11.1
	golang.org/x/sys v0.47.0
	k8s.io/api v0.0.0-20260728020008-a9c6d6c7c5a6
	k8s.io/apimachinery v0.36.0-alpha.2.0.20260723070748-eed236ceee2c
	k8s.io/client-go v0.0.0-20260728165410-2bf14f2d1167
	k8s.io/component-helpers v0.20.0-alpha.2.0.20260724062130-1961876b9c57
	k8s.io/dynamic-resource-allocation v0.26.0-beta.0.0.20260726112004-8bb18a99de19
	k8s.io/klog/v2 v2.140.0
	k8s.io/utils v0.0.0-20260626114624-be93311217bd
	sigs.k8s.io/yaml v1.6.0
	tags.cncf.io/container-device-interface v1.1.0
	tags.cncf.io/container-device-interface/specs-go v1.1.0
)

require (
	github.com/Masterminds/semver/v3 v3.4.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/containerd/log v0.1.0 // indirect
	github.com/containerd/ttrpc v1.2.7 // indirect
	github.com/davecgh/go-spew v1.1.2-0.20180830191138-d8f796af33cc // indirect
	github.com/emicklei/go-restful/v3 v3.13.0 // indirect
	github.com/fsnotify/fsnotify v1.5.1 // indirect
	github.com/fxamacker/cbor/v2 v2.9.1 // indirect
	github.com/go-openapi/jsonpointer v1.0.0 // indirect
	github.com/go-openapi/jsonreference v1.0.0 // indirect
	github.com/go-openapi/swag v0.27.1 // indirect
	github.com/go-openapi/swag/cmdutils v0.27.1 // indirect
	github.com/go-openapi/swag/conv v0.27.1 // indirect
	github.com/go-openapi/swag/fileutils v0.27.1 // indirect
	github.com/go-openapi/swag/jsonutils v0.27.1 // indirect
	github.com/go-openapi/swag/loading v0.27.1 // indirect
	github.com/go-openapi/swag/mangling v0.27.1 // indirect
	github.com/go-openapi/swag/netutils v0.27.1 // indirect
	github.com/go-openapi/swag/pools v0.27.1 // indirect
	github.com/go-openapi/swag/stringutils v0.27.1 // indirect
	github.com/go-openapi/swag/typeutils v0.27.1 // indirect
	github.com/go-openapi/swag/yamlutils v0.27.1 // indirect
	github.com/go-task/slim-sprig/v3 v3.0.0 // indirect
	github.com/google/gnostic-models v0.7.1 // indirect
	github.com/google/pprof v0.0.0-20260115054156-294ebfa9ad83 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/gorilla/websocket v1.5.4-0.20250319132907-e064f32e3674 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/knqyf263/go-plugin v0.9.0 // indirect
	github.com/kylelemons/godebug v1.1.0 // indirect
	github.com/moby/spdystream v0.5.1 // indirect
	github.com/moby/sys/capability v0.4.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.3-0.20250322232337-35a7c28c31ee // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/opencontainers/runtime-spec v1.3.0 // indirect
	github.com/opencontainers/runtime-tools v0.9.1-0.20251114084447-edf4cb3d2116 // indirect
	github.com/pmezard/go-difflib v1.0.1-0.20181226105442-5d4384ee4fb2 // indirect
	github.com/prometheus/procfs v0.21.1 // indirect
	github.com/sirupsen/logrus v1.9.3 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/tetratelabs/wazero v1.11.0 // indirect
	github.com/x448/float16 v0.8.4 // indirect
	go.etcd.io/etcd/client/pkg/v3 v3.7.0 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.27.1 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	go.yaml.in/yaml/v3 v3.0.4 // indirect
	golang.org/x/mod v0.37.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/term v0.45.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	golang.org/x/tools v0.47.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260526163538-3dc84a4a5aaa // indirect
	google.golang.org/grpc v1.82.1 // indirect
	google.golang.org/protobuf v1.36.12-0.20260120151049-f2248ac996af // indirect
	gopkg.in/evanphx/json-patch.v4 v4.13.0 // indirect
	gopkg.in/inf.v0 v0.9.1 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
	k8s.io/kube-openapi v0.0.0-20260721132016-d427ff9ee9ad // indirect
	k8s.io/kubelet v0.0.0-20260723065935-63b3249724f7 // indirect
	k8s.io/streaming v0.37.0-beta.0 // indirect
	sigs.k8s.io/json v0.0.0-20250730193827-2d320260d730 // indirect
	sigs.k8s.io/randfill v1.0.0 // indirect
	sigs.k8s.io/structured-merge-diff/v6 v6.4.2 // indirect
)
