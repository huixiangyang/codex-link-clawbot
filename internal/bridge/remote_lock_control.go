package bridge

import (
	"fmt"
	"strings"
)

func (h *Handler) lockRemote(userID string) string {
	if h.remoteLock == nil || !h.remoteLock.Enabled() {
		return "远程锁定未配置。请先在 security.remote_lock_code 设置解锁码并重启服务。"
	}
	h.deleteControlState(userID)
	queueWasPaused := false
	if h.tasks != nil {
		queueWasPaused = h.tasks.Status(userID).Paused
		if err := h.tasks.SetPaused(userID, true); err != nil {
			return fmt.Sprintf("远程锁定失败：无法暂停 codex-link-clawbot 请求队列：%v", err)
		}
	}
	if err := h.remoteLock.Lock(userID); err != nil {
		if h.tasks != nil && !queueWasPaused {
			_ = h.tasks.SetPaused(userID, false)
		}
		return fmt.Sprintf("远程锁定失败：%v", err)
	}
	if h.coordinator != nil {
		h.coordinator.Cancel(userID)
	}
	return "codex-link-clawbot 已远程锁定。后续消息和附件不会进入 Codex。发送“解锁 解锁码”恢复。"
}

func (h *Handler) confirmRemoteLock(userID string) string {
	if h.remoteLock == nil || !h.remoteLock.Enabled() {
		return "远程锁定未配置。请先在 security.remote_lock_code 设置解锁码并重启服务。"
	}
	if h.remoteLock.IsLocked(userID) {
		return "codex-link-clawbot 已处于远程锁定。发送“解锁 解锁码”恢复。"
	}
	options := []controlOption{{Code: "1", Label: "确认远程锁定", Action: actionRemoteLock}}
	if !h.storeChoiceWithBack(userID, viewSecurityLockConfirm, options, controlOption{Action: actionMain}) {
		return controlStateFailureResult().Text
	}
	return "准备远程锁定\n\n锁定会取消 codex-link-clawbot 当前执行、暂停请求队列，并阻止后续内容进入 Codex。\n\n" + renderControlOptions(options) + "\n\n回复 1 确认，0 返回操作总览。"
}

func (h *Handler) handleLockedInput(userID, text string) string {
	argument, matched := intentArgument(text, []string{"解锁"})
	if !matched || strings.TrimSpace(argument) == "" {
		return "codex-link-clawbot 当前已锁定。发送“解锁 解锁码”恢复。"
	}
	// 解锁码按字面比较，不能套用会话名称中的自然语言清洗。
	code := strings.Trim(strings.TrimSpace(argument), " \t\r\n：:，,。\"“”")
	if err := h.remoteLock.Unlock(userID, code); err != nil {
		return "解锁失败：解锁码不正确。"
	}
	return "codex-link-clawbot 已解锁。请求队列仍保持暂停；发送“继续队列”后才会恢复执行。"
}
