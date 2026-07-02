/*
Copyright 2026 The Kubernetes Authors.

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
	"bytes"
	"errors"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/kubernetes-sigs/dra-driver-cpu/pkg/sysfs"
)

type overlayFS struct {
	base  sysfs.FS
	files map[string][]byte
	dirs  map[string]map[string]overlayFileInfo
}

func newOverlayFS(base sysfs.FS, files map[string][]byte) sysfs.FS {
	overlay := &overlayFS{
		base:  base,
		files: files,
		dirs:  map[string]map[string]overlayFileInfo{".": {}},
	}
	overlay.buildDirectoryTree()
	return overlay
}

func (o *overlayFS) buildDirectoryTree() {
	for name, contents := range o.files {
		parts := strings.Split(name, "/")
		parent := "."
		for i, part := range parts {
			childPath := part
			if parent != "." {
				childPath = path.Join(parent, part)
			}

			info := overlayFileInfo{name: part, dir: i < len(parts)-1}
			if !info.dir {
				info.size = int64(len(contents))
			}
			o.dirs[parent][part] = info

			if info.dir {
				if _, ok := o.dirs[childPath]; !ok {
					o.dirs[childPath] = map[string]overlayFileInfo{}
				}
				parent = childPath
			}
		}
	}
}

func (o *overlayFS) lookup(name string) (overlayFileInfo, bool) {
	if data, ok := o.files[name]; ok {
		return overlayFileInfo{name: path.Base(name), size: int64(len(data))}, true
	}
	if _, ok := o.dirs[name]; ok {
		return overlayFileInfo{name: path.Base(name), dir: true}, true
	}
	return overlayFileInfo{}, false
}

func (o *overlayFS) Open(name string) (fs.File, error) {
	if err := validFSName("open", name); err != nil {
		return nil, err
	}
	info, ok := o.lookup(name)
	if !ok {
		return o.base.Open(name)
	}

	file := &overlayFile{path: name, info: info}
	if info.dir {
		entries, err := o.ReadDir(name)
		if err != nil {
			return nil, err
		}
		file.entries = entries
	} else {
		file.reader = bytes.NewReader(o.files[name])
	}
	return file, nil
}

func (o *overlayFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if err := validFSName("readdir", name); err != nil {
		return nil, err
	}
	overlayEntries, hasOverlay := o.dirs[name]
	if !hasOverlay {
		return fs.ReadDir(o.base, name)
	}

	entries := map[string]fs.DirEntry{}
	baseEntries, err := fs.ReadDir(o.base, name)
	if err != nil && !errors.Is(err, fs.ErrNotExist) && !errors.Is(err, syscall.ENOTDIR) {
		return nil, err
	}
	for _, entry := range baseEntries {
		entries[entry.Name()] = entry
	}
	for entryName, entry := range overlayEntries {
		entries[entryName] = entry
	}

	result := make([]fs.DirEntry, 0, len(entries))
	for _, entry := range entries {
		result = append(result, entry)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Name() < result[j].Name()
	})
	return result, nil
}

func (o *overlayFS) Lstat(name string) (fs.FileInfo, error) {
	if err := validFSName("lstat", name); err != nil {
		return nil, err
	}
	if info, ok := o.lookup(name); ok {
		return info, nil
	}
	return o.base.Lstat(name)
}

func (o *overlayFS) ReadLink(name string) (string, error) {
	if err := validFSName("readlink", name); err != nil {
		return "", err
	}
	if _, ok := o.lookup(name); ok {
		return "", &fs.PathError{Op: "readlink", Path: name, Err: syscall.EINVAL}
	}
	return o.base.ReadLink(name)
}

func validFSName(op, name string) error {
	if fs.ValidPath(name) {
		return nil
	}
	return &fs.PathError{Op: op, Path: name, Err: fs.ErrInvalid}
}

type overlayFileInfo struct {
	name string
	size int64
	dir  bool
}

func (i overlayFileInfo) Name() string       { return i.name }
func (i overlayFileInfo) Size() int64        { return i.size }
func (i overlayFileInfo) ModTime() time.Time { return time.Time{} }
func (i overlayFileInfo) IsDir() bool        { return i.dir }
func (i overlayFileInfo) Sys() any           { return nil }

func (i overlayFileInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0555
	}
	return 0444
}

func (i overlayFileInfo) Type() fs.FileMode { return i.Mode().Type() }

func (i overlayFileInfo) Info() (fs.FileInfo, error) { return i, nil }

type overlayFile struct {
	path    string
	info    overlayFileInfo
	reader  *bytes.Reader
	entries []fs.DirEntry
	offset  int
}

func (f *overlayFile) Close() error               { return nil }
func (f *overlayFile) Stat() (fs.FileInfo, error) { return f.info, nil }

func (f *overlayFile) Read(p []byte) (int, error) {
	if f.info.dir {
		return 0, &fs.PathError{Op: "read", Path: f.path, Err: syscall.EISDIR}
	}
	return f.reader.Read(p)
}

func (f *overlayFile) ReadDir(n int) ([]fs.DirEntry, error) {
	if !f.info.dir {
		return nil, &fs.PathError{Op: "readdir", Path: f.path, Err: syscall.ENOTDIR}
	}
	if n <= 0 {
		entries := f.entries[f.offset:]
		f.offset = len(f.entries)
		return entries, nil
	}
	if f.offset >= len(f.entries) {
		return nil, io.EOF
	}

	end := min(f.offset+n, len(f.entries))
	entries := f.entries[f.offset:end]
	f.offset = end
	if len(entries) < n {
		return entries, io.EOF
	}
	return entries, nil
}
