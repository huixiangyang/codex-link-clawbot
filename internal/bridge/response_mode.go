package bridge

import (
	"context"
	"fmt"

	"log"
	"strings"
	"unicode/utf8"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
)

const maxVoiceReplyScriptRunes = 2200

func (h *Handler) sendVoiceCodexReplySnapshot(ctx context.Context, client *ilink.Client, userID, reply, contextToken string) (bool, error) {
	if h.voice == nil {
		return false, fmt.Errorf("语音回答当前不可用")
	}
	if strings.TrimSpace(contextToken) == "" {
		return false, fmt.Errorf("发送语音回答必须使用当前线程的消息上下文令牌")
	}
	script, excerpted := buildVoiceReplyScript(reply)
	if script == "" {
		return false, fmt.Errorf("Codex 回答没有可朗读内容")
	}
	synthesis, err := h.voice.Generate(ctx, script)
	if err != nil {
		return false, err
	}
	mp3, err := EncodeVoiceMP3(ctx, h.voice.ffmpegCommand, synthesis.Audio)
	if err != nil {
		return false, err
	}

	// 语音独立发送；长内容只读节选，全文由用户从数字菜单取回。
	payloads := []outboundMediaPayload{{FileName: "codex-reply.mp3", Source: "codex-reply.mp3", Data: mp3, ContentType: "audio/mpeg"}}

	if err := sendMediaBatch(ctx, client, userID, contextToken, payloads); err != nil {
		return mediaBatchMayBeVisible(err), err
	}
	log.Printf("[voice] delivered Codex response mode excerpted=%t provider=%s for %s", excerpted, synthesis.ProviderID, ilink.LogLabel(userID))
	return true, nil
}

func buildVoiceReplyScript(reply string) (string, bool) {
	plain := strings.TrimSpace(MarkdownToPlainText(reply))
	if utf8.RuneCountInString(plain) <= maxVoiceReplyScriptRunes {
		return plain, false
	}
	suffix := "以上是回答节选。查看全文请回复数字零，再选数字五。"
	limit := maxVoiceReplyScriptRunes - utf8.RuneCountInString(suffix) - 2
	excerpt := truncateVoiceTextAtBoundary(plain, limit)
	return strings.TrimSpace(excerpt) + "\n\n" + suffix, true
}

func truncateVoiceTextAtBoundary(text string, limit int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= limit {
		return string(runes)
	}
	cut := limit
	for index := limit - 1; index >= limit/2; index-- {
		switch runes[index] {
		case '。', '！', '？', '\n':
			cut = index + 1
			return strings.TrimSpace(string(runes[:cut]))
		}
	}
	return strings.TrimSpace(string(runes[:cut]))
}
