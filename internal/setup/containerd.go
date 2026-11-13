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

// Adapted from dra-driver-memory d6a6c17, pkg/setup/containerd/containerd.go.
package setup

import (
	"bytes"
	"fmt"
	"io"
	"os"

	"github.com/pelletier/go-toml/v2"
)

const (
	ConfigNameStdio string = "-"
)

func Config(configName string) error {
	if configName == ConfigNameStdio {
		return ConfigStream(os.Stdin, os.Stdout)
	}
	return ConfigInplace(configName)
}

func ConfigStream(src io.Reader, dst io.Writer) error {
	data, err := io.ReadAll(src)
	if err != nil {
		return err
	}

	var conf map[string]any
	err = toml.Unmarshal(data, &conf)
	if err != nil {
		return err
	}

	if err := process(conf); err != nil {
		return err
	}

	b, err := toml.Marshal(conf)
	if err != nil {
		return err
	}

	_, err = dst.Write(b)
	if err != nil {
		return err
	}
	return nil
}

func ConfigInplace(confPath string) error {
	finfo, err := os.Lstat(confPath)
	if err != nil {
		return err
	}
	inData, err := os.ReadFile(confPath)
	if err != nil {
		return err
	}
	inBuf := bytes.NewBuffer(inData)
	outBuf := new(bytes.Buffer)
	err = ConfigStream(inBuf, outBuf)
	if err != nil {
		return err
	}
	return os.WriteFile(confPath, outBuf.Bytes(), finfo.Mode())
}

func process(conf map[string]any) error {
	plugins, ok := getMap(conf, "plugins")
	if !ok {
		return fmt.Errorf("containerd plugins table is missing; supply the node config.toml")
	}

	processNRI(plugins)

	key, err := runtimeKey(conf)
	if err != nil {
		return err
	}
	cri, ok := getMap(plugins, key)
	if !ok {
		return fmt.Errorf("containerd runtime table %q is missing; supply the node config.toml", key)
	}

	processCDI(cri)
	processHugepages(cri)
	return nil
}

func processNRI(plugins map[string]any) {
	plugins["io.containerd.nri.v1.nri"] = map[string]any{
		"disable":                     false,
		"disable_connections":         false,
		"plugin_config_path":          "/etc/nri/conf.d",
		"plugin_path":                 "/opt/nri/plugins",
		"plugin_registration_timeout": "5s",
		"plugin_request_timeout":      "5s",
		"socket_path":                 "/var/run/nri/nri.sock",
	}
}

func processCDI(cri map[string]any) {
	cri["enable_cdi"] = true
	cri["cdi_spec_dirs"] = []string{"/etc/cdi", "/var/run/cdi"}
}

func processHugepages(cri map[string]any) {
	cri["tolerate_missing_hugetlb_controller"] = false
	cri["disable_hugetlb_controller"] = false
}

func getMap(node map[string]any, key string) (map[string]any, bool) {
	subNode, ok := node[key]
	if !ok {
		return nil, false
	}
	subMap, ok := subNode.(map[string]any)
	if !ok {
		return nil, false
	}
	return subMap, true
}

func runtimeKey(conf map[string]any) (string, error) {
	switch conf["version"] {
	case int64(2):
		return "io.containerd.grpc.v1.cri", nil
	case int64(3):
		return "io.containerd.cri.v1.runtime", nil
	default:
		return "", fmt.Errorf("unsupported containerd config version %v; use version 2 or 3", conf["version"])
	}
}

func CheckRuntime(src io.Reader) error {
	data, err := io.ReadAll(src)
	if err != nil {
		return fmt.Errorf("read containerd configuration: %w", err)
	}
	var conf map[string]any
	if err := toml.Unmarshal(data, &conf); err != nil {
		return fmt.Errorf("decode containerd configuration: %w", err)
	}
	key, err := runtimeKey(conf)
	if err != nil {
		return err
	}
	plugins, ok := getMap(conf, "plugins")
	if !ok {
		return fmt.Errorf("containerd plugins table is missing; run runtime-config first")
	}
	cri, ok := getMap(plugins, key)
	if !ok {
		return fmt.Errorf("containerd %s table is missing; run runtime-config first", key)
	}
	for name, want := range map[string]bool{"enable_cdi": true, "disable_hugetlb_controller": false, "tolerate_missing_hugetlb_controller": false} {
		if got, ok := cri[name].(bool); !ok || got != want {
			return fmt.Errorf("containerd %s.%s must be %t; run runtime-config then restart containerd", key, name, want)
		}
	}
	nri, ok := getMap(plugins, "io.containerd.nri.v1.nri")
	if !ok {
		return fmt.Errorf("containerd NRI table is missing; run runtime-config first")
	}
	for _, name := range []string{"disable", "disable_connections"} {
		if got, ok := nri[name].(bool); !ok || got {
			return fmt.Errorf("containerd NRI %s must be false; run runtime-config then restart containerd", name)
		}
	}
	dirs, ok := cri["cdi_spec_dirs"].([]any)
	found := map[string]bool{}
	if ok {
		for _, dir := range dirs {
			if text, ok := dir.(string); ok {
				found[text] = true
			}
		}
	}
	if !found["/etc/cdi"] || !found["/var/run/cdi"] {
		return fmt.Errorf("containerd must watch /etc/cdi and /var/run/cdi; run runtime-config first")
	}
	return nil
}
