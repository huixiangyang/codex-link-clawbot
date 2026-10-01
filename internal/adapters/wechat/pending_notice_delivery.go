package wechat

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/delivery"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
)

const pendingNoticeDeliveryLimit = 4

func (h *Handler) flushPendingNotices(ctx context.Context, client *ilink.Client, ownerID, contextToken string) {
	if !h.noticeSending.CompareAndSwap(false, true) {
		return
	}
	defer h.noticeSending.Store(false)
	if h.pendingNotices == nil || client == nil || strings.TrimSpace(contextToken) == "" {
		return
	}
	notices, err := h.pendingNotices.List(ownerID, pendingNoticeDeliveryLimit)
	if err != nil {
		log.Printf("[notice] list pending notices for %s failed: %v", ilink.LogLabel(ownerID), err)
		return
	}
	if len(notices) == 0 {
		return
	}
	message := formatPendingNoticeDigest(notices)
	if err := SendTextReply(ctx, client, ownerID, message, contextToken, NewClientID()); err != nil {
		if outboundMayBeVisible(err) {
			// 响应不确定时不重复发送通知正文，避免用户下一次交互看到重复提醒。
			if completeErr := h.completePendingNotices(ownerID, notices); completeErr != nil {
				log.Printf("[notice] suppress ambiguous pending notices for %s failed: %v", ilink.LogLabel(ownerID), completeErr)
			}
		} else {
			log.Printf("[notice] pending notices remain deferred for %s: %v", ilink.LogLabel(ownerID), err)
		}
		return
	}
	if err := h.completePendingNotices(ownerID, notices); err != nil {
		log.Printf("[notice] complete delivered notices for %s failed: %v", ilink.LogLabel(ownerID), err)
	}
}

func (h *Handler) completePendingNotices(ownerID string, notices []delivery.Notice) error {
	ids := make([]string, 0, len(notices))
	for _, notice := range notices {
		ids = append(ids, notice.ID)
	}
	return h.pendingNotices.Complete(ownerID, ids)
}

func formatPendingNoticeDigest(notices []delivery.Notice) string {
	lines := []string{"有一项请求需要处理："}
	if len(notices) > 1 {
		lines[0] = fmt.Sprintf("有 %d 项请求需要处理：", len(notices))
	}
	for index, notice := range notices {
		lines = append(lines, "", fmt.Sprintf("[%d] %s", index+1, notice.Title), presentation.Truncate(strings.TrimSpace(notice.Body), 1200))
	}
	if len(notices) == pendingNoticeDeliveryLimit {
		lines = append(lines, "", "其余事项可在最近结果中查看。")
	}
	return strings.Join(lines, "\n")
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
