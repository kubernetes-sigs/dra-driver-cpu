//go:build !amd64

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

package device

import (
	"github.com/go-logr/logr"
)

// IMPORTANT NOTE: the code is functionally identical to the x86_64/amd64 variant,
// we only scan sysfs and make logic on arch-neutral data; but we don't have yet
// enough experience on and hw access to arm64 machine to fully support pcie
// processing on non-x86_64 hardware.

func ScanPCIeDevices(logger logr.Logger, sysfs SysFS, processDevice func(PCIeDevice) error) error {
	return nil
}

func PCIeDomainsFromFS(logger logr.Logger, sysfs SysFS) ([]PCIeDomain, error) {
	return nil, nil
}
