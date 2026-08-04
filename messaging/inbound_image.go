package messaging

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/ilink"
)

const (
	maxInboundImages     = 4
	maxInboundImageBytes = int64(20 << 20)
	defaultImagePrompt   = "请分析我发送的图片，并结合当前工作区完成图片中体现的任务。"
)

// prepareAgentInput 将微信图片落入一次性私有目录，并生成 Codex 可消费的本机图片输入。
// cleanup 必须在 turn 结束后调用，避免用户图片长期残留在磁盘。
func prepareAgentInput(ctx context.Context, text string, images []*ilink.ImageItem, root string) (agent.ChatRequest, func(), error) {
	request := agent.ChatRequest{Text: strings.TrimSpace(text)}
	cleanup := func() {}
	if len(images) == 0 {
		return request, cleanup, nil
	}
	if len(images) > maxInboundImages {
		return request, cleanup, fmt.Errorf("单条消息最多支持 %d 张图片", maxInboundImages)
	}
	if request.Text == "" {
		request.Text = defaultImagePrompt
	}

	if root == "" {
		var err error
		root, err = inboundImageRoot()
		if err != nil {
			return request, cleanup, err
		}
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return request, cleanup, fmt.Errorf("解析图片临时目录: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return request, cleanup, fmt.Errorf("创建图片临时目录: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return request, cleanup, fmt.Errorf("收紧图片临时目录权限: %w", err)
	}
	taskDir, err := os.MkdirTemp(root, "turn-")
	if err != nil {
		return request, cleanup, fmt.Errorf("创建图片任务目录: %w", err)
	}
	cleanup = func() {
		if err := os.RemoveAll(taskDir); err != nil {
			log.Printf("[image] failed to clean inbound image directory %s: %v", taskDir, err)
		}
	}

	for index, image := range images {
		data, err := downloadInboundImage(ctx, image)
		if err != nil {
			cleanup()
			return agent.ChatRequest{}, func() {}, fmt.Errorf("接收第 %d 张图片: %w", index+1, err)
		}
		ext, err := validatedImageExtension(data)
		if err != nil {
			cleanup()
			return agent.ChatRequest{}, func() {}, fmt.Errorf("校验第 %d 张图片: %w", index+1, err)
		}
		path := filepath.Join(taskDir, fmt.Sprintf("image-%02d%s", index+1, ext))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			cleanup()
			return agent.ChatRequest{}, func() {}, fmt.Errorf("保存第 %d 张图片: %w", index+1, err)
		}
		request.LocalImages = append(request.LocalImages, path)
	}

	log.Printf("[image] prepared %d inbound image(s) in %s", len(request.LocalImages), taskDir)
	return request, cleanup, nil
}

func inboundImageRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户目录: %w", err)
	}
	return filepath.Join(home, ".weclaw", "inbox"), nil
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
