package messaging

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/huixiangyang/weclaw/ilink"
	"github.com/huixiangyang/weclaw/visual"
)

const (
	visualReplyCacheTTL = 30 * time.Minute
	visualReplyMaxRunes = 40000
)

type documentVisualRenderer interface {
	RenderDocument(context.Context, visual.Document) (*visual.Artifact, error)
}

type cachedVisualReply struct {
	Text      string
	ExpiresAt time.Time
}

// SetVisualReplyConfig 设置长回复阅读卡片阈值；控制卡片不受此开关影响。
func (h *Handler) SetVisualReplyConfig(enabled bool, minRunes int) {
	h.visualReplyEnabled = enabled
	if minRunes > 0 {
		h.visualReplyMinRunes = minRunes
	}
}

func (h *Handler) sendVisualReply(ctx context.Context, client *ilink.Client, userID, reply, contextToken, clientID string) (bool, error) {
	if !h.visualReplyEnabled || h.visual == nil {
		return false, nil
	}
	renderer, ok := h.visual.(documentVisualRenderer)
	if !ok {
		return false, nil
	}
	runeCount := utf8.RuneCountInString(reply)
	if runeCount < h.visualReplyMinRunes || runeCount > visualReplyMaxRunes {
		return false, nil
	}
	documents := visual.PaginateMarkdown(reply)
	if len(documents) == 0 {
		return false, nil
	}

	artifacts := make([]*visual.Artifact, 0, len(documents))
	cleanupArtifacts := func() {
		for _, artifact := range artifacts {
			if artifact != nil && artifact.Cleanup != nil {
				artifact.Cleanup()
			}
		}
	}
	defer cleanupArtifacts()
	for _, document := range documents {
		artifact, err := renderer.RenderDocument(ctx, document)
		if err != nil {
			return false, fmt.Errorf("render page %d/%d: %w", document.PageNumber, document.TotalPages, err)
		}
		if artifact == nil || strings.TrimSpace(artifact.Path) == "" {
			if artifact != nil && artifact.Cleanup != nil {
				artifact.Cleanup()
			}
			return false, fmt.Errorf("render page %d/%d returned an empty artifact", document.PageNumber, document.TotalPages)
		}
		artifacts = append(artifacts, artifact)
	}
	for index, artifact := range artifacts {
		if err := SendMediaFromPath(ctx, client, userID, artifact.Path, contextToken); err != nil {
			document := documents[index]
			return false, fmt.Errorf("send page %d/%d: %w", document.PageNumber, document.TotalPages, err)
		}
	}

	h.visualReplies.Store(userID, &cachedVisualReply{Text: reply, ExpiresAt: time.Now().Add(visualReplyCacheTTL)})
	caption := fmt.Sprintf("Codex 回复已整理为 %d 页阅读卡片。回复“文字版”获取可复制原文。", len(documents))
	if err := SendTextReply(ctx, client, userID, caption, contextToken, clientID); err != nil {
		// 图片已经完整送达时不能回退并重复发送整篇原文。
		return true, fmt.Errorf("send reading caption: %w", err)
	}
	return true, nil
}

func (h *Handler) sendCachedVisualReply(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, text, clientID string) bool {
	if !isOneOf(text, "文字版", "回复原文", "查看原文", "复制原文", "可复制版本") {
		return false
	}
	value, ok := h.visualReplies.Load(msg.FromUserID)
	if !ok {
		return false
	}
	cached, ok := value.(*cachedVisualReply)
	if !ok || cached == nil || time.Now().After(cached.ExpiresAt) {
		h.visualReplies.CompareAndDelete(msg.FromUserID, value)
		return false
	}
	if err := SendTextReply(ctx, client, msg.FromUserID, cached.Text, msg.ContextToken, clientID); err != nil {
		log.Printf("[visual] failed to send cached text reply to %s: %v", msg.FromUserID, err)
	}
	return true
}
