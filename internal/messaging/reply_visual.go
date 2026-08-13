package messaging

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
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

func (h *Handler) sendVisualReplyWithStyle(ctx context.Context, client *ilink.Client, userID, reply, contextToken string, force bool, style visual.Style) (int, error) {
	// 显式阅读模式不受“自适应长回复”开关约束，只要求渲染能力可用。
	if h.visual == nil || (!force && !h.visualReplyEnabled) {
		return 0, nil
	}
	runeCount := utf8.RuneCountInString(reply)
	if (!force && runeCount < h.visualReplyMinRunes) || runeCount > visualReplyMaxRunes {
		return 0, nil
	}
	artifacts, documents, err := h.renderVisualDocumentsWithStyle(ctx, reply, style)
	if err != nil {
		return 0, err
	}
	cleanupArtifacts := func() {
		for _, artifact := range artifacts {
			if artifact != nil && artifact.Cleanup != nil {
				artifact.Cleanup()
			}
		}
	}
	defer cleanupArtifacts()
	payloads := make([]outboundMediaPayload, 0, len(artifacts))
	for index, artifact := range artifacts {
		payload, payloadErr := outboundMediaFromPath(artifact.Path)
		if payloadErr != nil {
			return 0, fmt.Errorf("prepare page %d/%d: %w", documents[index].PageNumber, documents[index].TotalPages, payloadErr)
		}
		payload.FileName = fmt.Sprintf("codex-link-clawbot-reply-%02d.png", index+1)
		payloads = append(payloads, payload)
	}

	// 发送前保留原文；若批次在发送阶段出现歧义，用户仍可取回完整文字。
	previousReply, hadPreviousReply := h.visualReplies.Load(userID)
	h.visualReplies.Store(userID, &cachedVisualReply{Text: reply, ExpiresAt: time.Now().Add(visualReplyCacheTTL)})
	if err := sendMediaBatch(ctx, client, userID, contextToken, payloads); err != nil {
		visible := mediaBatchVisibleCount(err)
		if visible == 0 {
			// 新批次完全不可见时恢复此前可取回的回复，不能让一次预上传失败抹掉历史成功结果。
			if hadPreviousReply {
				h.visualReplies.Store(userID, previousReply)
			} else {
				h.visualReplies.Delete(userID)
			}
		}
		return visible, fmt.Errorf("send reading pages: %w", err)
	}
	return len(documents), nil
}

func (h *Handler) renderVisualDocuments(ctx context.Context, userID, reply string) ([]*visual.Artifact, []visual.Document, error) {
	return h.renderVisualDocumentsWithStyle(ctx, reply, h.currentVisualStyle(userID))
}

func (h *Handler) renderVisualDocumentsWithStyle(ctx context.Context, reply string, style visual.Style) ([]*visual.Artifact, []visual.Document, error) {
	renderer, ok := h.visual.(documentVisualRenderer)
	if !ok {
		return nil, nil, fmt.Errorf("document renderer is unavailable")
	}
	documents := visual.PaginateMarkdown(reply)
	if len(documents) == 0 {
		return nil, nil, fmt.Errorf("reply cannot be paginated into reading cards")
	}
	artifacts := make([]*visual.Artifact, 0, len(documents))
	cleanup := func() {
		for _, artifact := range artifacts {
			if artifact != nil && artifact.Cleanup != nil {
				artifact.Cleanup()
			}
		}
	}
	for index := range documents {
		documents[index].Style = style
		artifact, err := renderer.RenderDocument(ctx, documents[index])
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("render page %d/%d: %w", documents[index].PageNumber, documents[index].TotalPages, err)
		}
		if artifact == nil || strings.TrimSpace(artifact.Path) == "" {
			if artifact != nil && artifact.Cleanup != nil {
				artifact.Cleanup()
			}
			cleanup()
			return nil, nil, fmt.Errorf("render page %d/%d returned an empty artifact", documents[index].PageNumber, documents[index].TotalPages)
		}
		artifacts = append(artifacts, artifact)
	}
	return artifacts, documents, nil
}

func (h *Handler) sendCachedVisualReply(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, text, clientID string) bool {
	if !isOneOf(text, "文字版", "回复原文", "查看原文", "复制原文", "可复制版本") {
		return false
	}
	// 原文取回是独立控制意图；无论缓存是否存在，都不能落入旧菜单或启动 Codex turn。
	h.deleteControlState(msg.FromUserID)
	value, ok := h.visualReplies.Load(msg.FromUserID)
	if !ok {
		h.sendVisualReplyExpired(ctx, client, msg, clientID)
		return true
	}
	cached, ok := value.(*cachedVisualReply)
	if !ok || cached == nil || time.Now().After(cached.ExpiresAt) {
		h.visualReplies.CompareAndDelete(msg.FromUserID, value)
		h.sendVisualReplyExpired(ctx, client, msg, clientID)
		return true
	}
	if err := SendTextReply(ctx, client, msg.FromUserID, cached.Text, msg.ContextToken, clientID); err != nil {
		log.Printf("[visual] failed to send cached text reply to %s: %v", ilink.LogLabel(msg.FromUserID), err)
	}
	return true
}

func (h *Handler) sendVisualReplyExpired(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, clientID string) {
	reply := "最近的阅读卡片原文已过期或不存在。请重新发送原问题，新的长回复会再次保留 30 分钟。"
	if err := SendTextReply(ctx, client, msg.FromUserID, reply, msg.ContextToken, clientID); err != nil {
		log.Printf("[visual] failed to send expired text notice to %s: %v", ilink.LogLabel(msg.FromUserID), err)
	}
}
