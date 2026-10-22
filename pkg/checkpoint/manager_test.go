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

// taken from k/k @ 975cf3742dec751d579f525c490b281fe927fa5d:
// includes code from
// - pkg/kubelet/checkpointmanager/checkpoint_manager_test.go.
//
// Local changes:
// - use real temporary directory, our borrowed code uses only the file store
// - use only a single version fake checkpoint, the borrowed checkpoint data
//   format is extensible (cpumanager checkpoint layout v4) so it is not necessary.

package checkpoint

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type FakeCheckpoint interface {
	Checkpoint
	GetData() ([]*PortMapping, bool)
}

// Data contains all types of data that can be stored in the checkpoint.
type Data struct {
	PortMappings []*PortMapping `json:"port_mappings,omitempty"`
	HostNetwork  bool           `json:"host_network,omitempty"`
}

type protocol string

// PortMapping is the port mapping configurations of a sandbox.
type PortMapping struct {
	// protocol of the port mapping.
	Protocol *protocol
	// Port number within the container.
	ContainerPort *int32
	// Port number on the host.
	HostPort *int32
	// Host ip to expose.
	HostIP string
}

// CheckpointData is a sample example structure to be used in test cases for checkpointing
type CheckpointData struct {
	Name     string
	Data     *Data
	Checksum Checksum
}

func newFakeCheckpoint(name string, portMappings []*PortMapping, hostNetwork bool) FakeCheckpoint {
	return &CheckpointData{
		Name: name,
		Data: &Data{
			PortMappings: portMappings,
			HostNetwork:  hostNetwork,
		},
	}
}

func (cp *CheckpointData) MarshalCheckpoint() ([]byte, error) {
	cp.Checksum = NewChecksum(*cp.Data)
	return json.Marshal(*cp)
}

func (cp *CheckpointData) UnmarshalCheckpoint(blob []byte) error {
	return json.Unmarshal(blob, cp)
}

func (cp *CheckpointData) VerifyChecksum() error {
	return cp.Checksum.Verify(*cp.Data)
}

func (cp *CheckpointData) GetData() ([]*PortMapping, bool) {
	return cp.Data.PortMappings, cp.Data.HostNetwork
}

func newTestCheckpointManagerWithDir(t *testing.T) (Manager, string) {
	t.Helper()
	dir := t.TempDir()
	manager, err := NewManager(dir)
	require.NoError(t, err)
	return manager, dir
}

func requireNoTemporaryFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	for _, entry := range entries {
		require.False(t, strings.HasPrefix(entry.Name(), "."), "temporary file %q was left behind", entry.Name())
	}
}

// tests borrowed from k/k

func TestCheckpointManagerBasicAPIs(t *testing.T) {
	manager, dir := newTestCheckpointManagerWithDir(t)
	require.Equal(t, dir, manager.CheckpointPath())

	port80 := int32(80)
	port443 := int32(443)
	proto := protocol("tcp")
	ip1234 := "1.2.3.4"

	portMappings := []*PortMapping{
		{
			Protocol:      &proto,
			ContainerPort: &port80,
			HostPort:      &port80,
			HostIP:        ip1234,
		},
		{
			Protocol:      &proto,
			ContainerPort: &port443,
			HostPort:      &port443,
			HostIP:        ip1234,
		},
	}
	checkpoint1 := newFakeCheckpoint("check1", portMappings, true)

	checkpoints := []struct {
		checkpointKey     string
		checkpoint        FakeCheckpoint
		expectHostNetwork bool
	}{
		{
			checkpointKey:     "key1",
			checkpoint:        checkpoint1,
			expectHostNetwork: true,
		},
		{
			checkpointKey:     "key2",
			checkpoint:        newFakeCheckpoint("check2", nil, false),
			expectHostNetwork: false,
		},
	}

	for _, tc := range checkpoints {
		// Test CreateCheckpoints
		err := manager.CreateCheckpoint(tc.checkpointKey, tc.checkpoint)
		assert.NoError(t, err)

		// Test GetCheckpoints
		checkpointOut := newFakeCheckpoint("", nil, false)
		err = manager.GetCheckpoint(tc.checkpointKey, checkpointOut)
		assert.NoError(t, err)
		actualPortMappings, actualHostNetwork := checkpointOut.GetData()
		expPortMappings, expHostNetwork := tc.checkpoint.GetData()
		assert.Equal(t, expPortMappings, actualPortMappings)
		assert.Equal(t, expHostNetwork, actualHostNetwork)
	}
	// Test corrupt checksum case
	checkpointOut := newFakeCheckpoint("", nil, false)
	err := manager.CreateCheckpoint("key1", checkpointOut)
	assert.NoError(t, err)
	err = manager.GetCheckpoint("key1", checkpoint1)
	assert.EqualError(t, err, "checkpoint is corrupted")

	// Test ListCheckpoints
	keys, err := manager.ListCheckpoints()
	assert.NoError(t, err)
	sort.Strings(keys)
	assert.Equal(t, []string{"key1", "key2"}, keys)

	// Test RemoveCheckpoints
	err = manager.RemoveCheckpoint("key1")
	assert.NoError(t, err)
	// Test Remove Nonexisted Checkpoints
	err = manager.RemoveCheckpoint("key1")
	assert.NoError(t, err)

	// Test ListCheckpoints
	keys, err = manager.ListCheckpoints()
	assert.NoError(t, err)
	assert.Equal(t, []string{"key2"}, keys)

	// Test Get NonExisted Checkpoint
	checkpointNE := newFakeCheckpoint("NE", nil, false)
	err = manager.GetCheckpoint("key1", checkpointNE)
	assert.ErrorIs(t, err, ErrCheckpointNotFound)
}

// original tests, not borrowed from k/k

func TestNOOPPCheckpointManagerBasicAPIs(t *testing.T) {
	manager := NewNOOPManager()
	require.Empty(t, manager.CheckpointPath())

	port80 := int32(80)
	port443 := int32(443)
	proto := protocol("tcp")
	ip1234 := "1.2.3.4"
	portMappings := []*PortMapping{
		{
			Protocol:      &proto,
			ContainerPort: &port80,
			HostPort:      &port80,
			HostIP:        ip1234,
		},
		{
			Protocol:      &proto,
			ContainerPort: &port443,
			HostPort:      &port443,
			HostIP:        ip1234,
		},
	}
	checkpoint := newFakeCheckpoint("noop1", portMappings, true)

	// must always fail
	chkpt1 := &CheckpointData{}
	err := manager.GetCheckpoint("key1", chkpt1)
	assert.ErrorIs(t, err, ErrCheckpointNotFound)
	assert.Nil(t, chkpt1.Data)

	err = manager.CreateCheckpoint("key2", checkpoint)
	require.NoError(t, err)

	keys, err := manager.ListCheckpoints()
	require.NoError(t, err)
	assert.Empty(t, keys)

	chkpt2 := &CheckpointData{}
	err = manager.GetCheckpoint("key2", chkpt2)
	assert.ErrorIs(t, err, ErrCheckpointNotFound)
	assert.Nil(t, chkpt2.Data)

	err = manager.RemoveCheckpoint("key2")
	require.NoError(t, err)
}

func TestCheckpointManagerMalformedCheckpoint(t *testing.T) {
	manager, dir := newTestCheckpointManagerWithDir(t)

	// intentionally write malformed json
	require.NoError(t, os.WriteFile(filepath.Join(dir, "corrupt"), []byte(`{"Name":"partial"`), 0600))

	out := &CheckpointData{}
	err := manager.GetCheckpoint("corrupt", out)

	require.Error(t, err)
	var syntaxErr *json.SyntaxError
	require.ErrorAs(t, err, &syntaxErr)
	require.NotErrorIs(t, err, ErrCheckpointNotFound)
	require.Empty(t, out.Name, "a malformed checkpoint must not be partially decoded")
	require.Nil(t, out.Data, "a malformed checkpoint must not be partially decoded")
}

func TestCheckpointManagerOverwriteData(t *testing.T) {
	manager, dir := newTestCheckpointManagerWithDir(t)

	port80 := int32(80)
	port443 := int32(443)
	proto := protocol("tcp")
	ip1234 := "1.2.3.4"
	portMappings := []*PortMapping{
		{
			Protocol:      &proto,
			ContainerPort: &port80,
			HostPort:      &port80,
			HostIP:        ip1234,
		},
		{
			Protocol:      &proto,
			ContainerPort: &port443,
			HostPort:      &port443,
			HostIP:        ip1234,
		},
	}
	first := newFakeCheckpoint("first", portMappings, false)
	second := newFakeCheckpoint("second", nil, true)
	require.NoError(t, manager.CreateCheckpoint("key", first))
	require.NoError(t, manager.CreateCheckpoint("key", second))

	out := &CheckpointData{}
	require.NoError(t, manager.GetCheckpoint("key", out))
	require.Equal(t, "second", out.Name)
	require.True(t, out.Data.HostNetwork)
	require.Empty(t, out.Data.PortMappings)

	want, err := second.MarshalCheckpoint()
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(dir, "key"))
	require.NoError(t, err)
	require.Equal(t, want, got)
	requireNoTemporaryFiles(t, dir)

	keys, err := manager.ListCheckpoints()
	require.NoError(t, err)
	require.Equal(t, []string{"key"}, keys)
}

func TestCheckpointManagerWriteFailureCleansTemporaryFile(t *testing.T) {
	manager, dir := newTestCheckpointManagerWithDir(t)

	// this kinda violates the blackbox testing principles because
	// we abuse the implementation detail of the checkpoint manager
	// which uses `os.Rename` to ensure atomic writes. The idea here
	// is that `rename` cannot handle existing directories with the
	// same name of the target file. While this indeed leverage an
	// implementation detail, it's also a cheap and easy way to simulate
	// a write error. Alternatives like injecting a faulty fileStore
	// will require more API changes.
	require.NoError(t, os.Mkdir(filepath.Join(dir, "alloc"), 0755))

	err := manager.CreateCheckpoint("alloc", newFakeCheckpoint("blocked", nil, false))

	var linkErr *os.LinkError
	require.ErrorAs(t, err, &linkErr)
	require.Equal(t, "rename", linkErr.Op)
	requireNoTemporaryFiles(t, dir)
}

func TestCheckpointManagerListIgnoresTemporaryFiles(t *testing.T) {
	manager, dir := newTestCheckpointManagerWithDir(t)
	require.NoError(t, manager.CreateCheckpoint("real", newFakeCheckpoint("real", nil, false)))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".partial"), []byte("leftover"), 0600))

	keys, err := manager.ListCheckpoints()

	require.NoError(t, err)
	require.Equal(t, []string{"real"}, keys)
}

func TestCheckpointManagerListFailsWhenDirectoryRemoved(t *testing.T) {
	manager, dir := newTestCheckpointManagerWithDir(t)
	require.NoError(t, os.RemoveAll(dir))

	keys, err := manager.ListCheckpoints()

	require.ErrorContains(t, err, "failed to list checkpoint store")
	require.Empty(t, keys)
}

type marshalFailingCheckpoint struct {
	Checkpoint
	err error
}

func (cp marshalFailingCheckpoint) MarshalCheckpoint() ([]byte, error) {
	return nil, cp.err
}

func TestCheckpointManagerMarshalFailureDoesNotMutateWrittenCheckpoint(t *testing.T) {
	port80 := int32(80)
	proto := protocol("tcp")
	ip1234 := "1.2.3.4"
	portMappings := []*PortMapping{
		{
			Protocol:      &proto,
			ContainerPort: &port80,
			HostPort:      &port80,
			HostIP:        ip1234,
		},
	}
	manager, dir := newTestCheckpointManagerWithDir(t)
	require.NoError(t, manager.CreateCheckpoint("key", newFakeCheckpoint("original", portMappings, true)))
	cause := errors.New("injected serialization failure")

	err := manager.CreateCheckpoint("key", marshalFailingCheckpoint{err: cause})
	require.ErrorIs(t, err, cause)

	out := &CheckpointData{}
	require.NoError(t, manager.GetCheckpoint("key", out))
	require.Equal(t, "original", out.Name)
	require.NotNil(t, out.Data)
	require.True(t, out.Data.HostNetwork)
	require.Equal(t, out.Data.PortMappings, portMappings)
	requireNoTemporaryFiles(t, dir)
}
