package execution

import (
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
)

func suggestedSessionName(request codex.ChatRequest) string {
	text := strings.TrimSpace(request.Text)
	if text != "" {
		if index := strings.IndexByte(text, '\n'); index >= 0 {
			text = text[:index]
		}
		text = strings.Join(strings.Fields(text), " ")
		return presentation.Truncate(text, 36)
	}
	if len(request.LocalImages) > 0 {
		return "图片分析"
	}
	if len(request.LocalFiles) > 0 {
		name := strings.TrimSpace(request.LocalFiles[0].Name)
		if name != "" {
			return presentation.Truncate("文件分析 · "+name, 36)
		}
		return "文件分析"
	}
	return ""
}

func isImageAnnotationIntent(text string) bool {
	normalized := normalizeMessageIntent(text)
	for _, marker := range []string{"批注图片", "标注图片", "批注这张图", "标注这张图", "在图上标注"} {
		if strings.Contains(normalized, normalizeMessageIntent(marker)) {
			return true
		}
	}
	return false
}

func normalizeMessageIntent(text string) string {
	replacer := strings.NewReplacer(" ", "", "\t", "", "\n", "", "，", "", "。", "", "！", "", "？", "", ",", "", ".", "", "!", "", "?", "")
	return replacer.Replace(strings.ToLower(strings.TrimSpace(text)))
}

func taskChatRequest(payload request.LoadedRequest, outbox string) codex.ChatRequest {
	request := codex.ChatRequest{Text: payload.Text, ArtifactDir: outbox}
	for _, image := range payload.Images {
		request.LocalImages = append(request.LocalImages, image.AbsolutePath)
	}
	for _, file := range payload.Files {
		request.LocalFiles = append(request.LocalFiles, codex.LocalFile{
			Path: file.AbsolutePath, Name: file.Name, ContentType: file.ContentType, Size: file.Size,
		})
	}
	if len(request.LocalImages) > 0 && isImageAnnotationIntent(request.Text) {
		request.Text = strings.TrimSpace(request.Text + "\n\n[codex-link-clawbot 图片批注模式]\n请先理解图片和用户意图，再生成一张带有清晰、克制、移动端可读批注的 PNG。必须把最终图片写入本次 codex-link-clawbot 交付目录并回传；不得覆盖入站原图。")
	}
	return request
}
