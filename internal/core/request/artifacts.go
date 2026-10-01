package request

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

const (
	maxOutboundArtifacts = maxResultArtifacts
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

// CollectArtifacts 只收集本次 turn 专属 outbox 内的常规文件。
// 不再解析回复中的任意绝对路径，避免误发工作区源码或凭据。
func CollectArtifacts(root string) (artifactCollection, error) {
	var collection artifactCollection
	if strings.TrimSpace(root) == "" {
		return collection, nil
	}
	if err := rejectArtifactSymlinks(root); err != nil {
		return collection, fmt.Errorf("解析交付目录: %w", err)
	}
	cleanRoot := filepath.Clean(root)
	info, err := os.Stat(cleanRoot)
	if err != nil || !info.IsDir() {
		return collection, fmt.Errorf("交付目录不可用")
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
		if info.Size() == 0 {
			collection.Skipped = append(collection.Skipped, rel+"（空文件）")
			return nil
		}
		if info.Size() > MaxFileBytes {
			collection.Skipped = append(collection.Skipped, rel+"（超过 50 MiB）")
			return nil
		}
		if len(collection.Paths) >= maxOutboundArtifacts {
			collection.Skipped = append(collection.Skipped, rel+"（超过 8 个文件）")
			return nil
		}
		if totalSize+info.Size() > MaxTaskBytes {
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

func AppendArtifactSummary(reply string, sentPaths, failed []string) string {
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
