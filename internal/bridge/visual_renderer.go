package bridge

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
)

// VisualRenderer 提供微信状态菜单和语音配图；阅读卡使用独立的文档渲染接口。
type VisualRenderer interface {
	Render(context.Context, visual.Card) (*visual.Artifact, error)
	RenderMenu(context.Context, visual.Menu) (*visual.Artifact, error)
}

// sendBridgeNotice 发送接收与拒绝等桥接状态；管理操作不会经过该通道。
func (h *Handler) sendBridgeNotice(ctx context.Context, client *ilink.Client, userID, message, contextToken, clientID string) error {
	if h.coordinator == nil {
		return SendTextReply(ctx, client, userID, message, contextToken, clientID)
	}
	if !h.coordinator.Effect(func(life context.Context) {
		sendCtx, cancel := context.WithTimeout(life, 20*time.Second)
		defer cancel()
		if err := SendTextReply(sendCtx, client, userID, message, contextToken, clientID); err != nil {
			log.Printf("[notice] delivery failed: %v", err)
		}
	}) {
		return fmt.Errorf("发送繁忙，请稍后重试")
	}
	return nil
}

func (h *Handler) currentVisualStyle(userID string) presentation.Style {
	if h.preferences == nil {
		return presentation.DefaultStyle
	}
	return h.preferences.Get(userID).Style
}
