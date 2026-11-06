/*
Copyright 2018 The Kubernetes Authors.

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
// - pkg/kubelet/checkpointmanager
// - pkg/kubelet/checkpointmanager/checksum
// - pkg/kubelet/checkpointmanager/errors.

package checkpoint

import (
	"errors"
	"fmt"
	"hash/fnv"
	"sync"

	"k8s.io/utils/dump"
)

// Checkpoint provides the process checkpoint data.
type Checkpoint interface {
	MarshalCheckpoint() ([]byte, error)
	UnmarshalCheckpoint(blob []byte) error
	VerifyChecksum() error
}

// Manager provides the interface to manage checkpoints.
type Manager interface {
	// CreateCheckpoint persists checkpoint in CheckpointStore. checkpointKey is the key for utilstore to locate checkpoint.
	// For file backed utilstore, checkpointKey is the file name to write the checkpoint data.
	CreateCheckpoint(checkpointKey string, checkpoint Checkpoint) error
	// GetCheckpoint retrieves checkpoint from CheckpointStore.
	GetCheckpoint(checkpointKey string, checkpoint Checkpoint) error
	// RemoveCheckpoint will not return error if checkpoint does not exist.
	RemoveCheckpoint(checkpointKey string) error
	// ListCheckpoints returns the list of existing checkpoints.
	ListCheckpoints() ([]string, error)
	// CheckpointPath returns the directory backing the manager, or an empty
	// string when checkpoint persistence is disabled.
	CheckpointPath() string
}

type noopImpl struct{}

func (mgr noopImpl) CreateCheckpoint(checkpointKey string, checkpoint Checkpoint) error {
	return nil
}

func (mgr noopImpl) GetCheckpoint(checkpointKey string, checkpoint Checkpoint) error {
	return ErrCheckpointNotFound
}

func (mgr noopImpl) RemoveCheckpoint(checkpointKey string) error {
	return nil
}

func (mgr noopImpl) ListCheckpoints() ([]string, error) {
	return nil, nil
}

func (mgr noopImpl) CheckpointPath() string {
	return ""
}

func NewNOOPManager() Manager {
	return noopImpl{}
}

type impl struct {
	path  string
	store *fileStore
	mutex sync.Mutex
}

// NewManager returns a new instance of a checkpoint manager.
func NewManager(checkpointDir string) (Manager, error) {
	store, err := newFileStore(checkpointDir)
	if err != nil {
		return nil, err
	}

	return &impl{path: checkpointDir, store: store}, nil
}

// CreateCheckpoint persists checkpoint in CheckpointStore.
func (manager *impl) CreateCheckpoint(checkpointKey string, checkpoint Checkpoint) error {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()

	blob, err := checkpoint.MarshalCheckpoint()
	if err != nil {
		return err
	}
	return manager.store.Write(checkpointKey, blob)
}

// GetCheckpoint retrieves checkpoint from CheckpointStore.
func (manager *impl) GetCheckpoint(checkpointKey string, checkpoint Checkpoint) error {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()

	blob, err := manager.store.Read(checkpointKey)
	if err != nil {
		if errors.Is(err, errKeyNotFound) {
			return ErrCheckpointNotFound
		}
		return err
	}

	err = checkpoint.UnmarshalCheckpoint(blob)
	if err == nil {
		err = checkpoint.VerifyChecksum()
	}
	return err
}

// RemoveCheckpoint will not return error if checkpoint does not exist.
func (manager *impl) RemoveCheckpoint(checkpointKey string) error {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()

	return manager.store.Delete(checkpointKey)
}

// ListCheckpoints returns the list of existing checkpoints.
func (manager *impl) ListCheckpoints() ([]string, error) {
	manager.mutex.Lock()
	defer manager.mutex.Unlock()

	keys, err := manager.store.List()
	if err != nil {
		return []string{}, fmt.Errorf("failed to list checkpoint store: %w", err)
	}
	return keys, nil
}

func (manager *impl) CheckpointPath() string {
	return manager.path
}

// Checksum is the data to be stored as checkpoint.
type Checksum uint64

// Verify verifies that passed checksum is same as calculated checksum.
func (cs Checksum) Verify(data any) error {
	actualCS := NewChecksum(data)
	if cs != actualCS {
		return &CorruptCheckpointError{ActualCS: uint64(actualCS), ExpectedCS: uint64(cs)}
	}
	return nil
}

// New returns the Checksum of checkpoint data.
func NewChecksum(data any) Checksum {
	return Checksum(getChecksum(data))
}

// Get returns calculated checksum of checkpoint data.
func getChecksum(data any) uint64 {
	hash := fnv.New32a()
	fmt.Fprintf(hash, "%v", dump.ForHash(data))
	return uint64(hash.Sum32())
}

// ErrCorruptCheckpoint error is reported when checksum does not match.
// Check for it with:
//
//	var csErr *CorruptCheckpointError
//	if errors.As(err, &csErr) { ... }
//	if errors.Is(err, CorruptCheckpointError{}) { ... }
type CorruptCheckpointError struct {
	ActualCS, ExpectedCS uint64
}

func (err CorruptCheckpointError) Error() string {
	return "checkpoint is corrupted"
}

func (err CorruptCheckpointError) Is(target error) bool {
	switch target.(type) {
	case *CorruptCheckpointError, CorruptCheckpointError:
		return true
	default:
		return false
	}
}

// ErrCheckpointNotFound is reported when checkpoint is not found for a given key.
var ErrCheckpointNotFound = fmt.Errorf("checkpoint is not found")
