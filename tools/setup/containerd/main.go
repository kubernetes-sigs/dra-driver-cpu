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

package main

import (
	"fmt"
	"os"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/setup/containerd"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: %s /path/to/config.toml\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "usage: %s - (read stdin, write stdout)\n", os.Args[0])
		os.Exit(1)
	}

	err := containerd.Config(os.Args[1])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error processing %q: %v\n", os.Args[1], err)
		os.Exit(127)
	}
}
