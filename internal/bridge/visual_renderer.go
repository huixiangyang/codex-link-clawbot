package bridge

import (
	"context"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
)

// VisualRenderer 只服务于 Codex 最终回答的阅读卡和语音配图，不承载微信控制菜单。
type VisualRenderer interface {
	Render(context.Context, visual.Card) (*visual.Artifact, error)
}

// sendBridgeNotice 发送可靠入队等桥接状态；管理操作不会经过该通道。
func (h *Handler) sendBridgeNotice(ctx context.Context, client *ilink.Client, userID, message, contextToken, clientID string) error {
	return SendTextReply(ctx, client, userID, message, contextToken, clientID)
}

func (h *Handler) currentVisualStyle(userID string) presentation.Style {
	if h.preferences == nil {
		return presentation.DefaultStyle
	}
	return h.preferences.Get(userID).Style
}
