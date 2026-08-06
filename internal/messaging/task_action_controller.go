package messaging

import "context"

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
		return h.requestTaskRetry(userID, option.Value).withIdentity(string(option.Action), DomainTask)
	case actionTaskContinueSession:
		text = h.continueTaskSession(ctx, userID, option.Value, option.Page)
	case actionTaskRerun:
		return h.requestSuccessfulTaskRerun(userID, option.Value, false).withIdentity(string(option.Action), DomainTask)
	case actionTaskRerunNewSession:
		return h.requestSuccessfulTaskRerun(userID, option.Value, true).withIdentity(string(option.Action), DomainTask)
	case actionTaskFrozenText:
		return h.requestFrozenTaskText(userID, option.Value).withIdentity(string(option.Action), DomainTask)
	case actionQueuePause:
		text = h.setQueuePaused(userID, true)
	case actionQueueResume:
		text = h.setQueuePaused(userID, false)
	case actionConfirmQueueClear:
		text = h.confirmClearQueue(userID)
	case actionQueueClear:
		text = h.clearQueue(userID)
	default:
		return invalidControlAction(option.Action, DomainTask)
	}
	return controlTextResult(option.Action, DomainTask, text)
}

func (h *Handler) dispatchTaskIntent(ctx context.Context, userID string, resolved ResolvedIntent) ActionResult {
	var text string
	switch resolved.Definition.ID {
	case IntentCancel:
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
				text = "当前没有正在执行的任务。发送 / 可以打开操作菜单。"
			}
		}
	case IntentTaskStatus:
		text = h.openTaskStatus(userID)
	case IntentTaskCenter:
		text = h.openActivities(userID, 1)
	case IntentTaskContinue:
		task, ok := h.latestSuccessfulTask(userID, false)
		if !ok {
			text = "还没有可以继续的成功任务。发送“任务中心”查看记录。"
		} else {
			text = h.continueTaskSession(ctx, userID, task.ID, 1)
		}
	case IntentTaskRerun, IntentTaskRerunNew:
		task, ok := h.latestSuccessfulTask(userID, true)
		if !ok {
			return intentTextResult(resolved, "最近没有仍可复用的成功纯文字任务。")
		}
		return h.requestSuccessfulTaskRerun(userID, task.ID, resolved.Definition.ID == IntentTaskRerunNew).
			withIdentity(string(resolved.Definition.ID), DomainTask)
	case IntentQueuePause:
		text = h.setQueuePaused(userID, true)
	case IntentQueueResume:
		text = h.setQueuePaused(userID, false)
	case IntentQueueClear:
		text = h.confirmClearQueue(userID)
	default:
		return invalidIntentResult(resolved)
	}
	return intentTextResult(resolved, text)
}
