// Package safeio provides small ownership-file and directory safety primitives.
package safeio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func Directory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("not a regular directory: %s", path)
	}
	return nil
}

func Read(path string) ([]byte, error) {
	f, err := OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 65536 {
		return nil, fmt.Errorf("invalid ownership/control file: %s", path)
	}
	return io.ReadAll(io.LimitReader(f, 65537))
}

func SyncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// WriteExclusive publishes a fully synced file without replacing any destination.
func WriteExclusive(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := Directory(dir); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".agit-publish-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Link(f.Name(), path); err != nil {
		return err
	}
	return SyncDir(dir)
}

func EnsureDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !os.IsExist(err) {
		return err
	}
	return Directory(path)
}

func ReadJSON(path string, value any) error {
	data, err := Read(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("invalid trailing control/ownership data")
	}
	return nil
}
func WriteJSON(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return WriteExclusive(path, data)
}
