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
// - pkg/kubelet/util/store/filestore_test.go
// - pkg/kubelet/util/store/store_test.go.
//
// Adapted to use a real temporary directory instead of the k/k filesystem
// abstraction, because the store copied here uses os directly. The upstream
// testStore helper is inlined into TestFileStore because it has one caller.
// TestValidateKey is extended with empty, "..", "../escape", and the key
// length boundary cases.
//
// We should not test private data. In this case though, we make an exception
// because we are borrowing code and test from kubernetes. Since we are using
// only internally that code we borrowed from kubernetes (file store) we made
// it private. And we kept the test using the convention Test_privateFunction.

package checkpoint

import (
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_fileStore(t *testing.T) {
	store, err := newFileStore(t.TempDir())
	require.NoError(t, err)

	testCases := []struct {
		key       string
		data      string
		expectErr bool
	}{
		{
			key:       "id1",
			data:      "data1",
			expectErr: false,
		},
		{
			key:       "id2",
			data:      "data2",
			expectErr: false,
		},
		{
			key:       "/id1",
			data:      "data1",
			expectErr: true,
		},
		{
			key:       ".id1",
			data:      "data1",
			expectErr: true,
		},
		{
			key:       "   ",
			data:      "data2",
			expectErr: true,
		},
		{
			key:       "___",
			data:      "data2",
			expectErr: true,
		},
	}

	// Test add data.
	for _, c := range testCases {
		t.Log("test case: ", c)
		_, err := store.Read(c.key)
		assert.Error(t, err)

		err = store.Write(c.key, []byte(c.data))
		if c.expectErr {
			assert.Error(t, err)
			continue
		}

		require.NoError(t, err)
		// Test read data by key.
		data, err := store.Read(c.key)
		require.NoError(t, err)
		assert.Equal(t, c.data, string(data))
	}

	// Test list keys.
	keys, err := store.List()
	assert.NoError(t, err)
	sort.Strings(keys)
	assert.Equal(t, []string{"id1", "id2"}, keys)

	// Test Delete data
	for _, c := range testCases {
		if c.expectErr {
			continue
		}

		err = store.Delete(c.key)
		require.NoError(t, err)
		_, err = store.Read(c.key)
		assert.ErrorIs(t, err, errKeyNotFound)
	}

	// Test delete non-existent key.
	err = store.Delete("id1")
	assert.NoError(t, err)

	// Test list keys.
	keys, err = store.List()
	require.NoError(t, err)
	assert.Empty(t, keys)
}

func Test_validateKey(t *testing.T) {
	testcases := []struct {
		key   string
		valid bool
	}{
		{
			key:   "    ",
			valid: false,
		},
		{
			key:   "/foo/bar",
			valid: false,
		},
		{
			key:   ".foo",
			valid: false,
		},
		{
			key:   "a78768279290d33d0b82eaea43cb8346f500057cb5bd250e88c97a5585385d66",
			valid: true,
		},
		{
			key:   "a7.87-6_8",
			valid: true,
		},
		{
			key:   "a7.87-677-",
			valid: false,
		},
		{
			key:   "",
			valid: false,
		},
		{
			key:   "..",
			valid: false,
		},
		{
			key:   "../escape",
			valid: false,
		},
		{
			key:   strings.Repeat("a", keyMaxLength),
			valid: true,
		},
		{
			key:   strings.Repeat("a", keyMaxLength+1),
			valid: false,
		},
	}

	for _, tc := range testcases {
		if tc.valid {
			assert.NoError(t, validateKey(tc.key))
		} else {
			assert.Error(t, validateKey(tc.key))
		}
	}
}
