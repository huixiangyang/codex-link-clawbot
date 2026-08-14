package bridge

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/delivery"
	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
)

const pendingNoticeDeliveryLimit = 4

func (h *Handler) flushPendingNotices(ctx context.Context, client *ilink.Client, ownerID, contextToken string) {
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
	lines := []string{fmt.Sprintf("codex-link-clawbot 待阅通知 · %d 条", len(notices)), "", "以下通知此前缺少有效微信发送上下文，现随本次消息补送。"}
	for index, notice := range notices {
		lines = append(lines, "", fmt.Sprintf("[%d] %s", index+1, notice.Title), presentation.Truncate(strings.TrimSpace(notice.Body), 1200))
	}
	if len(notices) == pendingNoticeDeliveryLimit {
		lines = append(lines, "", "仍有更多待阅通知，将在后续交互中继续送达。")
	}
	return strings.Join(lines, "\n")
}
