package bridge

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
)

const visualReplyMaxRunes = 40000

type documentVisualRenderer interface {
	RenderDocument(context.Context, visual.Document) (*visual.Artifact, error)
}

func (h *Handler) sendVisualReplyWithStyle(ctx context.Context, client *ilink.Client, userID, reply, contextToken string, force bool, style presentation.Style) (int, error) {
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

	if err := sendMediaBatch(ctx, client, userID, contextToken, payloads); err != nil {
		return mediaBatchVisibleCount(err), fmt.Errorf("send reading pages: %w", err)
	}
	return len(documents), nil
}

func (h *Handler) renderVisualDocumentsWithStyle(ctx context.Context, reply string, style presentation.Style) ([]*visual.Artifact, []visual.Document, error) {
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
