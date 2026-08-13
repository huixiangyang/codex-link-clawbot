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
	case DomainQueue:
		return h.executeTaskControlAction(ctx, userID, option)
	case DomainProject:
		return h.executeProjectControlAction(ctx, userID, option)
	case DomainSession:
		return h.executeSessionControlAction(ctx, userID, option)
	case DomainPreference:
		return h.executePreferenceControlAction(userID, option)
	case DomainDelivery:
		return h.executeDeliveryControlAction(userID, option)
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
		actionTaskFrozenText, actionRecentResult, actionQueuePause, actionQueueResume, actionConfirmQueueClear,
		actionQueueClear, actionVoiceBriefing:
		return DomainQueue
	case actionProjectCenter, actionSelectProject:
		return DomainProject
	case actionSessionMenu, actionCodexDevelopment, actionCodexCommands, actionCodexCommandPage,
		actionCodexSlashCommand, actionCodexUsage, actionCodexPermissions, actionCodexGoalStatus,
		actionCodexGlobalOverview, actionCodexGlobalThreadPage,
		actionCodexUseGlobalThread, actionCodexAccount, actionCodexModelOverview, actionPromptGlobalSearch,
		actionCurrentSession, actionPickSession, actionBrowseSessions,
		actionPromptSessionSearch, actionSessionPage, actionSessionDetail, actionUseSession,
		actionPromptNewSession, actionPromptRenameSession, actionConfirmArchive,
		actionArchiveCurrent, actionConfirmArchiveItem, actionArchiveItem,
		actionPickArchivedSession, actionRestoreSession, actionForkThread,
		actionToggleThreadPin, actionCompactThread, actionPromptThreadGoal,
		actionClearThreadGoal, actionPauseThreadGoal, actionResumeThreadGoal,
		actionReviewThread, actionReviewContinue, actionReviewAccept, actionReviewRerun,
		actionCodexCapabilities, actionConfirmDeleteThread,
		actionDeleteThread, actionThreadModels, actionSelectThreadModel,
		actionThreadEfforts, actionSelectThreadEffort:
		return DomainSession
	case actionDeliveryBox, actionDeliveryPage, actionDeliveryDetail, actionResendDelivery:
		return DomainDelivery
	case actionVisualStyles, actionSetVisualStyle, actionResponseModes, actionSetResponseMode:
		return DomainPreference
	case actionRemoteLock, actionConfirmRemoteLock:
		return DomainSecurity
	default:
		return DomainSystem
	}
}

func controlActionRequiresReceipt(action controlAction) bool {
	switch action {
	case actionUseSession, actionCodexUseGlobalThread, actionArchiveCurrent, actionArchiveItem, actionRestoreSession,
		actionForkThread, actionToggleThreadPin, actionCompactThread, actionClearThreadGoal,
		actionPauseThreadGoal, actionResumeThreadGoal, actionCodexSlashCommand,
		actionReviewThread, actionReviewContinue, actionReviewRerun,
		actionDeleteThread, actionSelectThreadModel, actionSelectThreadEffort,
		actionCancelTask, actionTaskMoveFront, actionTaskDelete, actionTaskRetry,
		actionTaskContinueSession, actionTaskRerun, actionTaskRerunNewSession,
		actionTaskFrozenText, actionQueuePause, actionQueueResume, actionQueueClear,
		actionSelectProject, actionResendDelivery, actionRemoteLock,
		actionVoiceBriefing, actionSetVisualStyle, actionSetResponseMode:
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
	case actionFunctionDirectory:
		text = h.openCommandDirectory(ctx, userID)
	case actionRuntimeInfo:
		text = h.openRuntimeInfo(userID)
	case actionNoReplyDiagnostic:
		text = h.buildNoReplyDiagnostic(userID)
	case actionResultsDeliveryCenter:
		text = h.openResultsDeliveryCenter(userID)
	case actionSettingsCenter:
		text = h.openSettingsCenter(userID)
	case actionConfigurationStatus:
		text = h.openConfigurationStatus(userID)
	case actionDiagnosticsCenter:
		text = h.openDiagnosticsCenter(userID)
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
