package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const snapshotManifestVersion = 1

var snapshotStateDirectories = map[string]bool{
	"accounts":   true,
	"deliveries": true,
	"inbox":      true,
	"tasks":      true,
}

type snapshotEntry struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	Mode   uint32 `json:"mode"`
	Size   int64  `json:"size,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

type snapshotManifest struct {
	Version int             `json:"version"`
	Entries []snapshotEntry `json:"entries"`
}

type deploymentSnapshot struct {
	Directory  string
	StatePath  string
	BinaryPath string
	UnitPath   string
	BinaryMode os.FileMode
	UnitMode   os.FileMode
	BinaryHash string
	UnitHash   string
	Manifest   snapshotManifest
}

func createDeploymentSnapshot(deploymentDir, stateRoot, binaryPath, unitPath string) (deploymentSnapshot, error) {
	snapshot := deploymentSnapshot{
		Directory:  deploymentDir,
		StatePath:  filepath.Join(deploymentDir, "state"),
		BinaryPath: filepath.Join(deploymentDir, "binary.old"),
		UnitPath:   filepath.Join(deploymentDir, "unit.old"),
		Manifest:   snapshotManifest{Version: snapshotManifestVersion},
	}
	if err := os.MkdirAll(snapshot.StatePath, 0o700); err != nil {
		return deploymentSnapshot{}, err
	}
	if err := os.Chmod(deploymentDir, 0o700); err != nil {
		return deploymentSnapshot{}, err
	}
	binaryInfo, err := checkedRegularFile(binaryPath)
	if err != nil {
		return deploymentSnapshot{}, fmt.Errorf("snapshot binary: %w", err)
	}
	snapshot.BinaryMode = binaryInfo.Mode().Perm()
	if err := copyRegularFile(binaryPath, snapshot.BinaryPath, 0o600); err != nil {
		return deploymentSnapshot{}, fmt.Errorf("snapshot binary: %w", err)
	}
	snapshot.BinaryHash, err = fileSHA256(snapshot.BinaryPath)
	if err != nil {
		return deploymentSnapshot{}, fmt.Errorf("hash snapshot binary: %w", err)
	}
	unitInfo, err := checkedRegularFile(unitPath)
	if err != nil {
		return deploymentSnapshot{}, fmt.Errorf("snapshot systemd unit: %w", err)
	}
	snapshot.UnitMode = unitInfo.Mode().Perm()
	if err := copyRegularFile(unitPath, snapshot.UnitPath, 0o600); err != nil {
		return deploymentSnapshot{}, fmt.Errorf("snapshot systemd unit: %w", err)
	}
	snapshot.UnitHash, err = fileSHA256(snapshot.UnitPath)
	if err != nil {
		return deploymentSnapshot{}, fmt.Errorf("hash snapshot systemd unit: %w", err)
	}
	if err := snapshotState(stateRoot, snapshot.StatePath, &snapshot.Manifest); err != nil {
		return deploymentSnapshot{}, err
	}
	if err := writePrivateJSONAtomic(filepath.Join(deploymentDir, "manifest.json"), snapshot.Manifest); err != nil {
		return deploymentSnapshot{}, err
	}
	configPath := filepath.Join(stateRoot, "config.json")
	if _, err := checkedRegularFile(configPath); err == nil {
		if err := copyRegularFile(configPath, filepath.Join(deploymentDir, "config.old"), 0o600); err != nil {
			return deploymentSnapshot{}, err
		}
	}
	return snapshot, nil
}

func snapshotState(stateRoot, destination string, manifest *snapshotManifest) error {
	stateRoot = filepath.Clean(stateRoot)
	info, err := os.Lstat(stateRoot)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("state root must be a real directory")
	}
	entries, err := os.ReadDir(stateRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		source := filepath.Join(stateRoot, name)
		target := filepath.Join(destination, name)
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entryInfo.Mode().IsRegular() && snapshotTopLevelFile(name):
			if err := snapshotTreeEntry(stateRoot, source, target, manifest); err != nil {
				return err
			}
		case entryInfo.IsDir() && snapshotStateDirectories[name]:
			if err := snapshotTreeEntry(stateRoot, source, target, manifest); err != nil {
				return err
			}
		}
	}
	sort.Slice(manifest.Entries, func(i, j int) bool { return manifest.Entries[i].Path < manifest.Entries[j].Path })
	return syncDirectoryPath(destination)
}

func snapshotTopLevelFile(name string) bool {
	return name != "weclaw.log" && name != "cutover-status.log" && name != ".state.lock"
}

func snapshotTreeEntry(root, source, destination string, manifest *snapshotManifest) error {
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	relative, err := filepath.Rel(root, source)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("invalid snapshot path")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("state snapshot rejects symbolic link %s", relative)
	}
	if info.IsDir() {
		if err := os.MkdirAll(destination, 0o700); err != nil {
			return err
		}
		manifest.Entries = append(manifest.Entries, snapshotEntry{Path: filepath.ToSlash(relative), Kind: "directory", Mode: uint32(info.Mode().Perm())})
		children, err := os.ReadDir(source)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := snapshotTreeEntry(root, filepath.Join(source, child.Name()), filepath.Join(destination, child.Name()), manifest); err != nil {
				return err
			}
		}
		return syncDirectoryPath(destination)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("state snapshot rejects non-regular file %s", relative)
	}
	if err := copyRegularFile(source, destination, 0o600); err != nil {
		return err
	}
	hash, err := fileSHA256(destination)
	if err != nil {
		return err
	}
	manifest.Entries = append(manifest.Entries, snapshotEntry{
		Path: filepath.ToSlash(relative), Kind: "file", Mode: uint32(info.Mode().Perm()), Size: info.Size(), SHA256: hash,
	})
	return nil
}

func restoreDeploymentSnapshot(snapshot deploymentSnapshot, stateRoot, binaryPath, unitPath string) error {
	if err := verifySnapshot(snapshot); err != nil {
		return fmt.Errorf("verify rollback snapshot: %w", err)
	}
	if err := clearManagedState(stateRoot); err != nil {
		return fmt.Errorf("clear migrated state: %w", err)
	}
	for _, entry := range snapshot.Manifest.Entries {
		source := filepath.Join(snapshot.StatePath, filepath.FromSlash(entry.Path))
		target := filepath.Join(stateRoot, filepath.FromSlash(entry.Path))
		switch entry.Kind {
		case "directory":
			if err := os.MkdirAll(target, os.FileMode(entry.Mode)); err != nil {
				return err
			}
			if err := os.Chmod(target, os.FileMode(entry.Mode)); err != nil {
				return err
			}
		case "file":
			if err := copyRegularFile(source, target, os.FileMode(entry.Mode)); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unknown snapshot entry kind %q", entry.Kind)
		}
	}
	if err := copyRegularFile(snapshot.BinaryPath, binaryPath, snapshot.BinaryMode); err != nil {
		return err
	}
	if err := copyRegularFile(snapshot.UnitPath, unitPath, snapshot.UnitMode); err != nil {
		return err
	}
	if err := syncDirectoryPath(stateRoot); err != nil {
		return err
	}
	return nil
}

func verifySnapshot(snapshot deploymentSnapshot) error {
	hash, err := fileSHA256(snapshot.BinaryPath)
	if err != nil {
		return fmt.Errorf("hash binary snapshot: %w", err)
	}
	if hash != snapshot.BinaryHash {
		return fmt.Errorf("binary snapshot hash mismatch")
	}
	hash, err = fileSHA256(snapshot.UnitPath)
	if err != nil {
		return fmt.Errorf("hash unit snapshot: %w", err)
	}
	if hash != snapshot.UnitHash {
		return fmt.Errorf("unit snapshot hash mismatch")
	}
	for _, entry := range snapshot.Manifest.Entries {
		if entry.Kind != "file" {
			continue
		}
		path := filepath.Join(snapshot.StatePath, filepath.FromSlash(entry.Path))
		hash, err := fileSHA256(path)
		if err != nil {
			return err
		}
		if hash != entry.SHA256 {
			return fmt.Errorf("snapshot hash mismatch for %s", entry.Path)
		}
	}
	return nil
}

func clearManagedState(stateRoot string) error {
	stateRoot = filepath.Clean(stateRoot)
	if !filepath.IsAbs(stateRoot) || stateRoot == string(filepath.Separator) {
		return fmt.Errorf("refusing unsafe state root")
	}
	entries, err := os.ReadDir(stateRoot)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Type().IsRegular() && snapshotTopLevelFile(entry.Name()) || entry.IsDir() && snapshotStateDirectories[entry.Name()] {
			target := filepath.Join(stateRoot, entry.Name())
			if err := os.RemoveAll(target); err != nil {
				return err
			}
		}
	}
	return nil
}

func checkedRegularFile(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	return info, nil
}

func copyRegularFile(source, destination string, mode os.FileMode) error {
	if _, err := checkedRegularFile(source); err != nil {
		return err
	}
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".weclaw-copy-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := io.Copy(temporary, input); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return err
	}
	if err := os.Chmod(destination, mode); err != nil {
		return err
	}
	return syncDirectoryPath(filepath.Dir(destination))
}

func removeSnapshotState(snapshot deploymentSnapshot) error {
	if snapshot.StatePath == "" || filepath.Base(snapshot.StatePath) != "state" || filepath.Dir(snapshot.StatePath) != snapshot.Directory {
		return errors.New("invalid private snapshot path")
	}
	return os.RemoveAll(snapshot.StatePath)
}
