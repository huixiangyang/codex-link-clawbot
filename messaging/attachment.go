package messaging

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	maxOutboundArtifacts     = 8
	maxOutboundArtifactBytes = int64(50 << 20)
	maxOutboundArtifactsSize = int64(100 << 20)
)

var supportedArtifactExts = []string{
	".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx",
	".zip", ".tar", ".gz", ".tgz", ".patch", ".diff",
	".txt", ".log", ".md", ".csv", ".json", ".yaml", ".yml",
	".png", ".jpg", ".jpeg", ".gif", ".webp",
	".mp4", ".mov",
}

type artifactCollection struct {
	Paths   []string
	Skipped []string
}

// collectArtifacts 只收集本次 turn 专属 outbox 内的常规文件。
// 不再解析回复中的任意绝对路径，避免误发工作区源码或凭据。
func collectArtifacts(root string) (artifactCollection, error) {
	var collection artifactCollection
	if strings.TrimSpace(root) == "" {
		return collection, nil
	}
	cleanRoot, err := canonicalizePath(root, true)
	if err != nil {
		return collection, fmt.Errorf("解析交付目录: %w", err)
	}
	var totalSize int64
	err = filepath.WalkDir(cleanRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			collection.Skipped = append(collection.Skipped, filepath.Base(path)+"（无法读取）")
			return nil
		}
		if path == cleanRoot || entry.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(cleanRoot, path)
		if relErr != nil {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			collection.Skipped = append(collection.Skipped, rel+"（不允许符号链接）")
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() {
			collection.Skipped = append(collection.Skipped, rel+"（不是常规文件）")
			return nil
		}
		if !isSupportedArtifactPath(path) {
			collection.Skipped = append(collection.Skipped, rel+"（文件类型不支持）")
			return nil
		}
		if info.Size() > maxOutboundArtifactBytes {
			collection.Skipped = append(collection.Skipped, rel+"（超过 50 MiB）")
			return nil
		}
		if len(collection.Paths) >= maxOutboundArtifacts {
			collection.Skipped = append(collection.Skipped, rel+"（超过 8 个文件）")
			return nil
		}
		if totalSize+info.Size() > maxOutboundArtifactsSize {
			collection.Skipped = append(collection.Skipped, rel+"（总大小超过 100 MiB）")
			return nil
		}
		totalSize += info.Size()
		collection.Paths = append(collection.Paths, path)
		return nil
	})
	if err != nil {
		return collection, fmt.Errorf("扫描交付目录: %w", err)
	}
	return collection, nil
}

func appendArtifactSummary(reply string, sentPaths, failed []string) string {
	var lines []string
	for _, path := range sentPaths {
		lines = append(lines, "已发送附件："+filepath.Base(path))
	}
	for _, item := range failed {
		lines = append(lines, "附件未发送："+item)
	}
	if len(lines) == 0 {
		return reply
	}
	if strings.TrimSpace(reply) == "" {
		return strings.Join(lines, "\n")
	}
	return strings.TrimSpace(reply) + "\n\n" + strings.Join(lines, "\n")
}

func isSupportedArtifactPath(path string) bool {
	return slices.Contains(supportedArtifactExts, strings.ToLower(filepath.Ext(path)))
}

func canonicalizePath(path string, mustExist bool) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if realPath, err := filepath.EvalSymlinks(absPath); err == nil {
		return filepath.Clean(realPath), nil
	} else if mustExist {
		return "", err
	}
	return filepath.Clean(absPath), nil
}
