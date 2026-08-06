package messaging

import "context"

// executeControlAction 将数字菜单动作送入唯一领域控制器。
func (h *Handler) executeControlAction(ctx context.Context, userID string, option controlOption) ActionResult {
	action := option.Action
	if action == "" {
		action = actionMain
		option.Action = action
	}
	domain := controlActionDomain(action)
	switch domain {
	case DomainTask:
		return h.executeTaskControlAction(ctx, userID, option)
	case DomainProject:
		return h.executeProjectControlAction(userID, option)
	case DomainSession:
		return h.executeSessionControlAction(ctx, userID, option)
	case DomainPreference:
		return h.executePreferenceControlAction(userID, option)
	case DomainLibrary:
		return h.executeLibraryControlAction(userID, option)
	case DomainAutomation:
		return h.executeAutomationControlAction(ctx, userID, option)
	case DomainSecurity:
		return h.executeSecurityControlAction(userID, option)
	default:
		return h.executeSystemControlAction(ctx, userID, option)
	}
}

func controlActionDomain(action controlAction) ActionDomain {
	switch action {
	case actionTaskStatus, actionConfirmCancelTask, actionCancelTask, actionActivityPage,
		actionActivityDetail, actionTaskMoveFront, actionTaskDelete, actionTaskRetry,
		actionTaskContinueSession, actionTaskRerun, actionTaskRerunNewSession,
		actionTaskFrozenText, actionQueuePause, actionQueueResume, actionConfirmQueueClear,
		actionQueueClear:
		return DomainTask
	case actionProjectCenter, actionSelectProject, actionProjectQuickTasks, actionRunQuickTask,
		actionWorkflowDetail, actionPromptWorkflowCreate, actionWorkflowCreate,
		actionPromptWorkflowRename, actionWorkflowRename, actionPromptWorkflowEdit,
		actionWorkflowEdit, actionConfirmWorkflowDelete, actionWorkflowDelete,
		actionPromptWorkflowSave, actionWorkflowSave:
		return DomainProject
	case actionSessionMenu, actionCurrentSession, actionPickSession, actionBrowseSessions,
		actionPromptSessionSearch, actionSessionPage, actionSessionDetail, actionUseSession,
		actionPromptNewSession, actionPromptRenameSession, actionConfirmArchive,
		actionArchiveCurrent, actionConfirmArchiveItem, actionArchiveItem,
		actionPickArchivedSession, actionRestoreSession:
		return DomainSession
	case actionLibraryCenter, actionLibraryPage, actionLibraryDetail, actionResendDelivery:
		return DomainLibrary
	case actionAutomations, actionAutomation, actionRunAutomation, actionVoiceBriefing:
		return DomainAutomation
	case actionVisualStyles, actionSetVisualStyle, actionResponseModes, actionSetResponseMode:
		return DomainPreference
	case actionRemoteLock:
		return DomainSecurity
	default:
		return DomainSystem
	}
}

func controlActionRequiresReceipt(action controlAction) bool {
	switch action {
	case actionUseSession, actionArchiveCurrent, actionArchiveItem, actionRestoreSession,
		actionCancelTask, actionTaskMoveFront, actionTaskDelete, actionTaskRetry,
		actionTaskContinueSession, actionTaskRerun, actionTaskRerunNewSession,
		actionTaskFrozenText, actionQueuePause, actionQueueResume, actionQueueClear,
		actionSelectProject, actionRunQuickTask, actionWorkflowCreate, actionWorkflowRename,
		actionWorkflowEdit, actionWorkflowDelete, actionWorkflowSave, actionResendDelivery, actionRemoteLock,
		actionVoiceBriefing, actionRunAutomation, actionSetVisualStyle, actionSetResponseMode:
		return true
	default:
		return false
	}
}

func controlTextResult(action controlAction, domain ActionDomain, text string) ActionResult {
	return newActionResult(string(action), domain, text)
}

func invalidControlAction(action controlAction, domain ActionDomain) ActionResult {
	if action == "" {
		action = actionMain
	}
	return controlTextResult(action, domain, "这个操作已经失效。发送 / 重新打开菜单。")
}

func (h *Handler) executeSystemControlAction(ctx context.Context, userID string, option controlOption) ActionResult {
	var text string
	switch option.Action {
	case actionExit:
		text = "已退出菜单。直接发送文字、图片或文件即可交给 Codex。"
	case actionMain:
		text = h.openMainMenu(ctx, userID)
	case actionRuntimeInfo:
		text = h.openRuntimeInfo(userID)
	case actionNoReplyDiagnostic:
		text = h.buildNoReplyDiagnostic(userID)
	case actionMore:
		text = h.openMoreMenu(userID)
	case actionGuide:
		text = h.openGuide(userID)
	default:
		return invalidControlAction(option.Action, DomainSystem)
	}
	return controlTextResult(option.Action, DomainSystem, text)
}

func (h *Handler) dispatchSystemIntent(ctx context.Context, userID string, resolved ResolvedIntent) ActionResult {
	var text string
	switch resolved.Definition.ID {
	case IntentMenu:
		text = h.openMainMenu(ctx, userID)
	case IntentGuide:
		text = h.openGuide(userID)
	case IntentRuntime:
		text = h.openRuntimeInfo(userID)
	case IntentNoReplyDiagnostic:
		text = h.buildNoReplyDiagnostic(userID)
	default:
		return invalidIntentResult(resolved)
	}
	return intentTextResult(resolved, text)
}
