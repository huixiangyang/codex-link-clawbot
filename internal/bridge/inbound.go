package bridge

import (
	"bytes"
	"context"
	"fmt"

	"net/http"

	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
)

const (
	maxInboundImages     = 4
	maxInboundImageBytes = int64(20 << 20)
	maxInboundFiles      = 8
	maxInboundFileBytes  = int64(50 << 20)
	maxInboundTotalBytes = int64(100 << 20)
	defaultImagePrompt   = "请分析我发送的图片，并结合当前工作区完成图片中体现的要求。"
	defaultFilePrompt    = "请检查我发送的文件，并结合当前工作区完成文件中体现的要求。"
)

var inboundTextExtensions = map[string]bool{
	".txt": true, ".log": true, ".md": true, ".csv": true,
	".json": true, ".yaml": true, ".yml": true, ".xml": true,
	".toml": true, ".ini": true, ".diff": true, ".patch": true,
	".go": true, ".py": true, ".js": true, ".jsx": true,
	".ts": true, ".tsx": true, ".java": true, ".kt": true,
	".rs": true, ".c": true, ".h": true, ".cpp": true, ".hpp": true,
	".cs": true, ".php": true, ".rb": true, ".swift": true,
	".sh": true, ".bash": true, ".zsh": true, ".sql": true,
	".html": true, ".css": true, ".vue": true, ".svelte": true,
}

var inboundBinaryExtensions = map[string]bool{
	".pdf": true, ".zip": true, ".tar": true, ".gz": true, ".tgz": true, ".tar.gz": true,
}

type inboundDownloaders struct {
	image func(context.Context, *ilink.ImageItem) ([]byte, error)
	file  func(context.Context, *ilink.FileItem) ([]byte, error)
}

func downloadInboundImage(ctx context.Context, image *ilink.ImageItem) ([]byte, error) {
	if image == nil {
		return nil, fmt.Errorf("图片内容为空")
	}
	if int64(image.MidSize) > maxInboundImageBytes+16 {
		return nil, fmt.Errorf("图片超过 20 MiB 限制")
	}
	if strings.TrimSpace(image.URL) != "" {
		data, _, err := downloadFileLimited(ctx, image.URL, maxInboundImageBytes)
		return data, err
	}
	if image.Media == nil || strings.TrimSpace(image.Media.EncryptQueryParam) == "" || strings.TrimSpace(image.Media.AESKey) == "" {
		return nil, fmt.Errorf("微信消息没有可用的图片下载信息")
	}
	return DownloadFileFromCDN(ctx, image.Media.EncryptQueryParam, image.Media.AESKey, maxInboundImageBytes)
}

func downloadInboundFile(ctx context.Context, file *ilink.FileItem) ([]byte, error) {
	if err := validateInboundFileMetadata(file); err != nil {
		return nil, err
	}
	if strings.TrimSpace(file.URL) != "" {
		data, _, err := downloadFileLimited(ctx, file.URL, maxInboundFileBytes)
		return data, err
	}
	if file.Media == nil || strings.TrimSpace(file.Media.EncryptQueryParam) == "" || strings.TrimSpace(file.Media.AESKey) == "" {
		return nil, fmt.Errorf("微信消息没有可用的文件下载信息")
	}
	return DownloadFileFromCDN(ctx, file.Media.EncryptQueryParam, file.Media.AESKey, maxInboundFileBytes)
}

func validateInboundFileMetadata(file *ilink.FileItem) error {
	if file == nil {
		return fmt.Errorf("文件内容为空")
	}
	if strings.TrimSpace(file.FileName) == "" {
		return fmt.Errorf("文件名为空")
	}
	if strings.TrimSpace(file.Len) == "" {
		return nil
	}
	size, err := strconv.ParseInt(file.Len, 10, 64)
	if err != nil || size < 0 {
		return fmt.Errorf("文件大小字段无效")
	}
	if size > maxInboundFileBytes {
		return fmt.Errorf("文件超过 50 MiB 限制")
	}
	return nil
}

func validateInboundFile(rawName string, data []byte) (string, string, error) {
	if len(data) == 0 {
		return "", "", fmt.Errorf("文件为空")
	}
	if int64(len(data)) > maxInboundFileBytes {
		return "", "", fmt.Errorf("文件超过 50 MiB 限制")
	}
	name := sanitizeInboundFilename(rawName)
	ext := supportedInboundExtension(name)
	if ext == "" {
		return "", "", fmt.Errorf("不支持文件类型 %q", filepath.Ext(name))
	}
	if hasExecutableMagic(data) {
		return "", "", fmt.Errorf("拒绝可执行文件")
	}
	contentType := http.DetectContentType(data)
	if inboundTextExtensions[ext] {
		if bytes.IndexByte(data, 0) >= 0 {
			return "", "", fmt.Errorf("文本或代码文件包含二进制内容")
		}
		return name, contentType, nil
	}
	switch ext {
	case ".pdf":
		if contentType != "application/pdf" {
			return "", "", fmt.Errorf("文件扩展名与 PDF 内容不匹配")
		}
	case ".zip":
		if contentType != "application/zip" {
			return "", "", fmt.Errorf("文件扩展名与 ZIP 内容不匹配")
		}
	case ".gz", ".tgz", ".tar.gz":
		if contentType != "application/x-gzip" && contentType != "application/gzip" {
			return "", "", fmt.Errorf("文件扩展名与 GZip 内容不匹配")
		}
	}
	return name, contentType, nil
}

func sanitizeInboundFilename(raw string) string {
	raw = strings.ReplaceAll(raw, "\\", "/")
	name := strings.TrimSpace(filepath.Base(raw))
	var builder strings.Builder
	for _, r := range name {
		if unicode.IsControl(r) || r == '/' || r == '\\' || r == ':' {
			builder.WriteByte('_')
			continue
		}
		builder.WriteRune(r)
	}
	name = strings.TrimSpace(builder.String())
	if name == "" || name == "." {
		return "file"
	}
	if len([]byte(name)) > 180 {
		ext := filepath.Ext(name)
		stem := strings.TrimSuffix(name, ext)
		budget := 180 - len([]byte(ext))
		for len([]byte(stem)) > budget {
			_, size := utf8.DecodeLastRuneInString(stem)
			stem = stem[:len(stem)-size]
		}
		name = stem + ext
	}
	return name
}

func supportedInboundExtension(name string) string {
	lower := strings.ToLower(name)
	if strings.HasSuffix(lower, ".tar.gz") {
		return ".tar.gz"
	}
	ext := strings.ToLower(filepath.Ext(lower))
	if inboundTextExtensions[ext] || inboundBinaryExtensions[ext] {
		return ext
	}
	return ""
}

func hasExecutableMagic(data []byte) bool {
	return bytes.HasPrefix(data, []byte{0x7f, 'E', 'L', 'F'}) ||
		bytes.HasPrefix(data, []byte{'M', 'Z'})
}

func validatedImageExtension(data []byte) (string, error) {
	if len(data) == 0 {
		return "", fmt.Errorf("图片文件为空")
	}
	if int64(len(data)) > maxInboundImageBytes {
		return "", fmt.Errorf("图片超过 20 MiB 限制")
	}

	switch contentType := http.DetectContentType(data); contentType {
	case "image/jpeg":
		return ".jpg", nil
	case "image/png":
		return ".png", nil
	case "image/gif":
		return ".gif", nil
	case "image/webp":
		return ".webp", nil
	default:
		return "", fmt.Errorf("不支持的图片格式 %q，仅支持 JPEG、PNG、GIF、WebP", contentType)
	}
}
