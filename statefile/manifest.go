package statefile

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const ManifestVersion = 1

type ManifestEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Version int             `json:"version"`
	Entries []ManifestEntry `json:"entries"`
}

// BuildManifest 为明确列出的私有状态生成稳定清单，不遍历或记录清单外路径。
func BuildManifest(root string, relativePaths []string, maxTotalBytes int64) (Manifest, error) {
	root = filepath.Clean(root)
	if !filepath.IsAbs(root) || maxTotalBytes <= 0 {
		return Manifest{}, wrap(CategorySchema, "build manifest", root, errors.New("absolute root and positive capacity are required"))
	}
	manifest := Manifest{Version: ManifestVersion, Entries: make([]ManifestEntry, 0, len(relativePaths))}
	seen := make(map[string]bool, len(relativePaths))
	var total int64
	for _, relative := range relativePaths {
		relative = filepath.Clean(strings.TrimSpace(relative))
		if relative == "." || filepath.IsAbs(relative) || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || seen[relative] {
			return Manifest{}, wrap(CategorySchema, "build manifest", relative, errors.New("invalid or duplicate manifest path"))
		}
		seen[relative] = true
		path := filepath.Join(root, relative)
		file, size, err := openRegularFile(path)
		if err != nil {
			return Manifest{}, err
		}
		total += size
		if total > maxTotalBytes {
			_ = file.Close()
			return Manifest{}, wrap(CategoryCapacity, "build manifest", root, fmt.Errorf("manifest exceeds %d bytes", maxTotalBytes))
		}
		hash := sha256.New()
		if _, err := io.Copy(hash, file); err != nil {
			_ = file.Close()
			return Manifest{}, wrapSystem("hash", path, err)
		}
		if err := file.Close(); err != nil {
			return Manifest{}, wrapSystem("close", path, err)
		}
		manifest.Entries = append(manifest.Entries, ManifestEntry{Path: filepath.ToSlash(relative), Size: size, SHA256: fmt.Sprintf("%x", hash.Sum(nil))})
	}
	sort.Slice(manifest.Entries, func(i, j int) bool { return manifest.Entries[i].Path < manifest.Entries[j].Path })
	return manifest, nil
}

func VerifyManifest(root string, manifest Manifest, maxTotalBytes int64) error {
	if manifest.Version != ManifestVersion || maxTotalBytes <= 0 {
		return wrap(CategorySchema, "verify manifest", root, errors.New("invalid manifest schema or capacity"))
	}
	paths := make([]string, 0, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		paths = append(paths, filepath.FromSlash(entry.Path))
	}
	current, err := BuildManifest(root, paths, maxTotalBytes)
	if err != nil {
		return err
	}
	if len(current.Entries) != len(manifest.Entries) {
		return wrap(CategoryCorrupt, "verify manifest", root, errors.New("manifest entry count mismatch"))
	}
	for index := range current.Entries {
		if current.Entries[index] != manifest.Entries[index] {
			return wrap(CategoryCorrupt, "verify manifest", filepath.Join(root, filepath.FromSlash(manifest.Entries[index].Path)), errors.New("state hash or size mismatch"))
		}
	}
	return nil
}

// CopyManifestFiles 将已校验的清单文件复制到私有目录，供离线迁移备份使用。
func CopyManifestFiles(sourceRoot, destinationRoot string, manifest Manifest, maxTotalBytes int64) error {
	if err := VerifyManifest(sourceRoot, manifest, maxTotalBytes); err != nil {
		return err
	}
	if err := ensurePrivateDirectory(destinationRoot); err != nil {
		return err
	}
	for _, entry := range manifest.Entries {
		relative := filepath.FromSlash(entry.Path)
		source := filepath.Join(sourceRoot, relative)
		destination := filepath.Join(destinationRoot, relative)
		file, _, err := openRegularFile(source)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(file, entry.Size+1))
		closeErr := file.Close()
		if err != nil {
			return wrapSystem("read backup source", source, err)
		}
		if closeErr != nil {
			return wrapSystem("close backup source", source, closeErr)
		}
		if int64(len(data)) != entry.Size {
			return wrap(CategoryCorrupt, "copy backup", source, errors.New("source size changed"))
		}
		if err := Write(destination, data, Options{MaxBytes: maxTotalBytes}); err != nil {
			return err
		}
	}
	return WriteJSON(filepath.Join(destinationRoot, "manifest.json"), manifest, Options{MaxBytes: DefaultMaxBytes})
}

// RemoveManifestBackup 只删除由 CopyManifestFiles 创建并再次校验过的备份目录。
func RemoveManifestBackup(root string, manifest Manifest, maxTotalBytes int64) error {
	if err := VerifyManifest(root, manifest, maxTotalBytes); err != nil {
		return err
	}
	for index := len(manifest.Entries) - 1; index >= 0; index-- {
		path := filepath.Join(root, filepath.FromSlash(manifest.Entries[index].Path))
		if err := os.Remove(path); err != nil {
			return wrapSystem("remove backup", path, err)
		}
	}
	return nil
}
