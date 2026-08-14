package bridge

import (
	"context"

	"github.com/huixiangyang/codex-link-clawbot/internal/control"
)

// executeControlAction 将数字菜单动作送入唯一领域控制器。
func (h *Handler) executeControlAction(ctx context.Context, userID string, option controlOption) ActionResult {
	action := option.Action
	if action == "" {
		action = actionMain
		option.Action = action
	}
	domain := controlActionDomain(action)
	switch domain {
	case control.DomainQueue:
		return h.executeTaskControlAction(ctx, userID, option)
	case control.DomainProject:
		return h.executeProjectControlAction(ctx, userID, option)
	case control.DomainSession:
		return h.executeSessionControlAction(ctx, userID, option)
	case control.DomainPreference:
		return h.executePreferenceControlAction(userID, option)
	case control.DomainDelivery:
		return h.executeDeliveryControlAction(userID, option)
	case control.DomainSecurity:
		return h.executeSecurityControlAction(userID, option)
	default:
		return h.executeSystemControlAction(ctx, userID, option)
	}
}

func controlActionDomain(action controlAction) control.Domain {
	switch action {
	case actionTaskStatus, actionConfirmCancelTask, actionCancelTask, actionActivityPage,
		actionActivityDetail, actionTaskMoveFront, actionTaskDelete, actionTaskRetry,
		actionTaskContinueSession, actionTaskRerun, actionTaskRerunNewSession,
		actionTaskFrozenText, actionRecentResult, actionQueuePause, actionQueueResume, actionConfirmQueueClear,
		actionQueueClear, actionVoiceBriefing:
		return control.DomainQueue
	case actionProjectCenter, actionSelectProject:
		return control.DomainProject
	case actionSessionMenu, actionCodexDevelopment, actionCodexCommands, actionCodexCommandPage,
		actionCodexCommand, actionCodexUsage, actionCodexPermissions, actionCodexGoalStatus,
		actionCodexGlobalOverview, actionCodexGlobalThreadPage,
		actionCodexUseGlobalThread, actionCodexAccount, actionCodexModelOverview, actionPromptGlobalSearch,
		actionCurrentSession, actionThreadRelations, actionPickSession, actionBrowseSessions,
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
		return control.DomainSession
	case actionDeliveryBox, actionDeliveryPage, actionDeliveryDetail, actionResendDelivery:
		return control.DomainDelivery
	case actionVisualStyles, actionSetVisualStyle, actionResponseModes, actionSetResponseMode:
		return control.DomainPreference
	case actionRemoteLock, actionConfirmRemoteLock:
		return control.DomainSecurity
	default:
		return control.DomainSystem
	}
}

func controlActionRequiresReceipt(action controlAction) bool {
	switch action {
	case actionUseSession, actionCodexUseGlobalThread, actionArchiveCurrent, actionArchiveItem, actionRestoreSession,
		actionForkThread, actionToggleThreadPin, actionCompactThread, actionClearThreadGoal,
		actionPauseThreadGoal, actionResumeThreadGoal, actionCodexCommand,
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

func controlTextResult(action controlAction, domain control.Domain, text string) ActionResult {
	return newActionResult(string(action), domain, text)
}

func invalidControlAction(action controlAction, domain control.Domain) ActionResult {
	if action == "" {
		action = actionMain
	}
	return controlTextResult(action, domain, "这个操作已经失效。发送“菜单”重新打开操作总览。")
}

func (h *Handler) executeSystemControlAction(ctx context.Context, userID string, option controlOption) ActionResult {
	var text string
	switch option.Action {
	case actionExit:
		text = "已退出菜单。直接发送文字、图片或文件即可交给 Codex。"
	case actionMain:
		return pageActionResult(string(option.Action), control.DomainSystem, h.openMainMenuPage(ctx, userID))
	case actionFunctionDirectory:
		return pageActionResult(string(option.Action), control.DomainSystem, h.buildCommandDirectory(ctx, userID))
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
		return invalidControlAction(option.Action, control.DomainSystem)
	}
	return controlTextResult(option.Action, control.DomainSystem, text)
}

func (h *Handler) dispatchSystemIntent(ctx context.Context, userID string, resolved control.Resolved) ActionResult {
	var text string
	switch resolved.Definition.ID {
	case control.IntentMenu:
		return pageActionResult(string(resolved.Definition.ID), resolved.Definition.Domain, h.openMainMenuPage(ctx, userID))
	case control.IntentGuide:
		text = h.openGuide(userID)
	case control.IntentRuntime:
		text = h.openRuntimeInfo(userID)
	case control.IntentNoReplyDiagnostic:
		text = h.buildNoReplyDiagnostic(userID)
	default:
		return invalidIntentResult(resolved)
	}
	return intentTextResult(resolved, text)
}
