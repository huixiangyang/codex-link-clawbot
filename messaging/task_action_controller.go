package messaging

func (h *Handler) executeTaskControlAction(userID string, option controlOption) ActionResult {
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

func (h *Handler) dispatchTaskIntent(userID string, resolved ResolvedIntent) ActionResult {
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
