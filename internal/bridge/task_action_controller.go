package bridge

import (
	"context"

	"github.com/huixiangyang/codex-link-clawbot/internal/control"
)

func (h *Handler) executeTaskControlAction(ctx context.Context, userID string, option controlOption) ActionResult {
	var text string
	switch option.Action {
	case actionTaskStatus:
		text = h.openTaskStatus(userID)
	case actionConfirmCancelTask:
		text = h.confirmCancelTask(userID)
	case actionCancelTask:
		text = h.cancelActiveTask(userID)
	case actionActivityPage:
		text = h.openActivities(userID, option.Page)
	case actionActivityDetail:
		text = h.openActivityDetail(userID, option.Value, option.Page)
	case actionTaskMoveFront:
		text = h.moveTaskToFront(userID, option.Value, option.Page)
	case actionTaskDelete:
		text = h.deleteTask(userID, option.Value, option.Page)
	case actionTaskRetry:
		return h.requestTaskRetry(userID, option.Value).withIdentity(string(option.Action), control.DomainQueue)
	case actionTaskContinueSession:
		text = h.continueTaskSession(ctx, userID, option.Value, option.Page)
	case actionTaskRerun:
		return h.requestSuccessfulTaskRerun(userID, option.Value, false).withIdentity(string(option.Action), control.DomainQueue)
	case actionTaskRerunNewSession:
		return h.requestSuccessfulTaskRerun(userID, option.Value, true).withIdentity(string(option.Action), control.DomainQueue)
	case actionTaskFrozenText:
		return h.requestFrozenTaskText(userID, option.Value).withIdentity(string(option.Action), control.DomainQueue)
	case actionRecentResult:
		if task, exists := h.latestSuccessfulTask(userID, false); exists {
			text = h.openActivityDetail(userID, task.ID, 1)
		} else {
			text = "最近还没有成功执行记录。直接发送内容即可开始。"
		}
	case actionVoiceBriefing:
		return h.requestVoiceBriefing(userID).withIdentity(string(option.Action), control.DomainQueue)
	case actionQueuePause:
		text = h.setQueuePaused(userID, true)
	case actionQueueResume:
		text = h.setQueuePaused(userID, false)
	case actionConfirmQueueClear:
		text = h.confirmClearQueue(userID)
	case actionQueueClear:
		text = h.clearQueue(userID)
	default:
		return invalidControlAction(option.Action, control.DomainQueue)
	}
	return controlTextResult(option.Action, control.DomainQueue, text)
}

func (h *Handler) dispatchTaskIntent(ctx context.Context, userID string, resolved control.Resolved) ActionResult {
	var text string
	switch resolved.Definition.ID {
	case control.IntentCancel:
		if h.hasActiveTask(userID) {
			h.deleteControlState(userID)
			text = h.cancelActiveTask(userID)
		} else {
			_, status, err := h.loadControlState(userID)
			switch {
			case err != nil:
				text = controlStateFailureResult().Text
			case status == controlStateActive && h.deleteControlState(userID):
				text = "已退出当前操作。直接发送内容即可交给 Codex。"
			case status == controlStateActive:
				text = controlStateFailureResult().Text
			default:
				text = "codex-link-clawbot 当前没有正在执行的请求。发送“菜单”可以打开操作总览。"
			}
		}
	case control.IntentTaskStatus:
		text = h.openTaskStatus(userID)
	case control.IntentTaskCenter:
		text = h.openActivities(userID, 1)
	case control.IntentTaskContinue:
		task, ok := h.latestSuccessfulTask(userID, false)
		if !ok {
			text = "还没有可以继续的成功执行记录。发送“请求队列”查看记录。"
		} else {
			text = h.continueTaskSession(ctx, userID, task.ID, 1)
		}
	case control.IntentTaskRerun, control.IntentTaskRerunNew:
		task, ok := h.latestSuccessfulTask(userID, true)
		if !ok {
			return intentTextResult(resolved, "最近没有仍可复用的成功纯文字请求。")
		}
		return h.requestSuccessfulTaskRerun(userID, task.ID, resolved.Definition.ID == control.IntentTaskRerunNew).
			withIdentity(string(resolved.Definition.ID), control.DomainQueue)
	case control.IntentQueuePause:
		text = h.setQueuePaused(userID, true)
	case control.IntentQueueResume:
		text = h.setQueuePaused(userID, false)
	case control.IntentQueueClear:
		text = h.confirmClearQueue(userID)
	case control.IntentVoiceBriefing:
		return h.requestVoiceBriefing(userID).withIdentity(string(resolved.Definition.ID), control.DomainQueue)
	default:
		return invalidIntentResult(resolved)
	}
	return intentTextResult(resolved, text)
}
