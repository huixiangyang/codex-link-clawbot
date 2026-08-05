package cmd

import (
	"fmt"
	"os"
	"path/filepath"
)

func writeAtomicFile(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".weclaw-write-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	closeWithError := func(cause error) error {
		if closeErr := temporary.Close(); cause == nil {
			return closeErr
		}
		return cause
	}
	if err := temporary.Chmod(mode); err != nil {
		return closeWithError(err)
	}
	if _, err := temporary.Write(data); err != nil {
		return closeWithError(err)
	}
	if err := temporary.Sync(); err != nil {
		return closeWithError(err)
	}
	if err := closeWithError(nil); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	return syncDirectoryPath(directory)
}

func syncDirectoryPath(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return fmt.Errorf("sync directory: %w", err)
	}
	return directory.Close()
}
