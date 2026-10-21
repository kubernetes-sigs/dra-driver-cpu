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

package driverconfig

import (
	"flag"

	"github.com/go-logr/logr"
)

type FlagSource struct {
	overrides map[string]any
}

func FromFlags(flagset *flag.FlagSet) FlagSource {
	fs := FlagSource{
		overrides: make(map[string]any),
	}
	flagset.Visit(func(f *flag.Flag) {
		key, ok := flagToJSONKey[f.Name]
		if !ok {
			return
		}
		if g, ok := f.Value.(flag.Getter); ok {
			fs.overrides[key] = g.Get()
		} else {
			fs.overrides[key] = f.Value.String()
		}
	})
	return fs
}

func (fs FlagSource) Name() string {
	return "flags"
}

func (fs FlagSource) Apply(logger logr.Logger, cfg *Config) error {
	logger.V(6).Info("overrides", "stage", fs.Name(), "values", fs.overrides)
	return applyMap(cfg, fs.overrides)
}

// AddFlags registers the Config fields that are exposed as CLI flags on fs.
func (c *Config) AddFlags(fs *flag.FlagSet) {
	c.applyDefaults()

	fs.StringVar(&c.Kubeconfig, "kubeconfig", c.Kubeconfig, "absolute path to the kubeconfig file")
	fs.StringVar(&c.BindAddress, "bind-address", c.BindAddress, "The address to bind the HTTP server for /healthz and /metrics endpoints")
	fs.BoolVar(&c.ExposePCIeRoots, "expose-pcie-roots", c.ExposePCIeRoots, "Discover and expose PCIe roots as device attributes. Requires the DRAListTypeAttributes=true Feature Gate in the cluster.")
	fs.StringVar(&c.KubeletRootDir, "kubelet-root-dir", c.KubeletRootDir, "The kubelet root directory. The plugin registration and plugin data directories are derived from it as <root>/plugins_registry and <root>/plugins/<driver-name>. The Helm chart supplies this together with the matching hostPath mounts; set it only if the kubelet --root-dir is not the default /var/lib/kubelet.")
}

func (c *Config) applyDefaults() {
	defaults := Default()
	if c.BindAddress == "" {
		c.BindAddress = defaults.BindAddress
	}
	if c.KubeletRootDir == "" {
		c.KubeletRootDir = defaults.KubeletRootDir
	}
}
