package messaging

import (
	"context"
	"fmt"

	"github.com/huixiangyang/weclaw/ilink"
)

// presentActionResult 是控制动作唯一的微信副作用出口。
func (h *Handler) presentActionResult(ctx context.Context, client *ilink.Client, msg ilink.WeixinMessage, result ActionResult, clientID string) error {
	if err := result.validate(); err != nil {
		return fmt.Errorf("invalid action result: %w", err)
	}
	switch result.Effect.Kind {
	case EffectEnqueuePrompt:
		return h.enqueueCodexTaskInProject(
			ctx, client, msg, result.Effect.Value, nil, nil, clientID,
			result.Effect.ProjectID, result.Effect.ThreadID, result.Effect.NewThread,
		)
	case EffectRetryTask:
		return h.retryCodexTask(ctx, client, msg, result.Effect.Value, clientID)
	case EffectFrozenText:
		h.sendFrozenTaskText(ctx, client, msg, result.Effect.Value, clientID)
		return nil
	case EffectSendMedia:
		if err := SendMediaFromPath(ctx, client, msg.FromUserID, result.Effect.Value, msg.ContextToken); err != nil {
			result.Text = fmt.Sprintf("交付物发送失败：%v", err)
		}
	case EffectVoiceBriefing:
		if _, err := h.sendVoiceBriefing(ctx, client, msg.FromUserID, msg.ContextToken); err != nil {
			result.Text = fmt.Sprintf("语音简报生成失败：%v", err)
			return h.sendControlReply(ctx, client, msg.FromUserID, result.Text, msg.ContextToken, clientID)
		}
		// 成功时阅读卡和 MP3 已完整交付，不再追加低信息量状态卡。
		return nil
	}
	return h.sendControlReply(ctx, client, msg.FromUserID, result.Text, msg.ContextToken, clientID)
}
