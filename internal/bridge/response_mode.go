package bridge

import (
	"context"
	"fmt"
	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
)

const maxVoiceReplyScriptRunes = 2200

func (h *Handler) currentResponseMode(userID string) presentation.ResponseMode {
	if h.preferences == nil {
		return presentation.ResponseAdaptive
	}
	return h.preferences.Get(userID).ResponseMode
}

func (h *Handler) sendVoiceCodexReply(ctx context.Context, client *ilink.Client, userID, reply, contextToken string) (bool, error) {
	projectName := "未配置"
	if h.projects != nil {
		projectName = h.projects.Current(userID).Name
	}
	return h.sendVoiceCodexReplySnapshot(ctx, client, userID, reply, contextToken, h.currentVisualStyle(userID), projectName)
}

func (h *Handler) sendVoiceCodexReplySnapshot(ctx context.Context, client *ilink.Client, userID, reply, contextToken string, style presentation.Style, projectName string) (bool, error) {
	if h.voice == nil || h.visual == nil {
		return false, fmt.Errorf("语音回答当前不可用")
	}
	if strings.TrimSpace(contextToken) == "" {
		return false, fmt.Errorf("发送语音回答必须使用当前线程的消息上下文令牌")
	}
	script, summarized := buildVoiceReplyScript(reply)
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

	var artifacts []*visual.Artifact
	cleanup := func() {
		for _, artifact := range artifacts {
			if artifact != nil && artifact.Cleanup != nil {
				artifact.Cleanup()
			}
		}
	}
	defer cleanup()
	var payloads []outboundMediaPayload
	if summarized {
		documentArtifacts, _, renderErr := h.renderVisualDocumentsWithStyle(ctx, reply, style)
		if renderErr != nil {
			return false, fmt.Errorf("渲染语音模式完整阅读卡: %w", renderErr)
		}
		artifacts = append(artifacts, documentArtifacts...)
		for _, artifact := range documentArtifacts {
			payload, payloadErr := outboundMediaFromPath(artifact.Path)
			if payloadErr != nil {
				return false, payloadErr
			}
			payloads = append(payloads, payload)
		}
	}

	if strings.TrimSpace(projectName) == "" {
		projectName = "未配置"
	}
	footer := "配套 MP3 音频文件随后发送"
	if summarized {
		footer = "完整回答见前页阅读卡，配套语音摘要随后发送"
	}
	card := visual.Card{
		Variant: visual.VariantSystem,
		Style:   style,
		Title:   "语音回答",
		Facts: []visual.Fact{
			{Label: "Codex 工作空间", Value: projectName},
			{Label: "音频来源", Value: synthesis.ProviderID},
		},
		Body:   []string{script},
		Footer: footer,
	}
	companion, err := h.visual.Render(ctx, card)
	if err != nil {
		return false, fmt.Errorf("渲染语音回答卡: %w", err)
	}
	if companion == nil || strings.TrimSpace(companion.Path) == "" {
		if companion != nil && companion.Cleanup != nil {
			companion.Cleanup()
		}
		return false, fmt.Errorf("语音回答卡渲染器未生成图片")
	}
	artifacts = append(artifacts, companion)
	companionPayload, err := outboundMediaFromPath(companion.Path)
	if err != nil {
		return false, err
	}
	payloads = append(payloads,
		companionPayload,
		outboundMediaPayload{FileName: "codex-link-clawbot-reply.mp3", Source: "codex-link-clawbot-reply.mp3", Data: mp3, ContentType: "audio/mpeg"},
	)

	// 发送阶段开始后响应可能丢失，先保留完整原文，避免用户看到部分卡片却无法取回文字版。
	h.visualReplies.Store(userID, &cachedVisualReply{Text: reply, ExpiresAt: time.Now().Add(visualReplyCacheTTL)})
	if err := sendMediaBatch(ctx, client, userID, contextToken, payloads); err != nil {
		return mediaBatchMayBeVisible(err), err
	}
	log.Printf("[voice] delivered Codex response mode summarized=%t provider=%s for %s", summarized, synthesis.ProviderID, ilink.LogLabel(userID))
	return true, nil
}

func buildVoiceReplyScript(reply string) (string, bool) {
	plain := strings.TrimSpace(MarkdownToPlainText(reply))
	if utf8.RuneCountInString(plain) <= maxVoiceReplyScriptRunes {
		return plain, false
	}
	suffix := "回答内容较长，完整内容已放在前面的阅读卡中。"
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
