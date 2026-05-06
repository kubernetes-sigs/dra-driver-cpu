/*
 * Copyright The Kubernetes Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package containerd

import (
	"bytes"
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

	process(conf)

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

func process(conf map[string]any) {
	plugins, ok := getMap(conf, "plugins")
	if !ok {
		return
	}

	processNRI(plugins)

	cri, ok := getMap(plugins, "io.containerd.grpc.v1.cri")
	if !ok {
		return
	}

	processCDI(cri)
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
