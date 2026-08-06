package messaging

import "context"

func (h *Handler) executeSessionControlAction(ctx context.Context, userID string, option controlOption) ActionResult {
	var text string
	switch option.Action {
	case actionSessionMenu:
		text = h.openSessionMenu(ctx, userID)
	case actionCurrentSession:
		text = h.currentSessionDetail(ctx, userID)
	case actionPickSession:
		text = h.openSessionPicker(ctx, userID, false, "")
	case actionBrowseSessions:
		text = h.openSessionBrowser(ctx, userID, false, "")
	case actionPromptSessionSearch:
		text = h.promptSessionSearch(userID)
	case actionSessionPage:
		text = h.openSessionPickerPage(ctx, userID, option.Archived, option.Query, option.Page, option.AutoUse)
	case actionSessionDetail:
		text = h.sessionDetail(ctx, userID, option)
	case actionUseSession:
		if h.hasActiveTask(userID) {
			text = mutationBusyText()
		} else {
			text = h.useSession(ctx, userID, option.Value)
		}
	case actionPromptNewSession:
		if h.hasActiveTask(userID) {
			text = mutationBusyText()
		} else {
			text = h.promptNewSessionName(userID)
		}
	case actionPromptRenameSession:
		if h.hasActiveTask(userID) {
			text = mutationBusyText()
		} else {
			text = h.promptRenameSession(userID)
		}
	case actionConfirmArchive:
		if h.hasActiveTask(userID) {
			text = mutationBusyText()
		} else {
			text = h.confirmArchiveCurrent(ctx, userID)
		}
	case actionArchiveCurrent:
		if h.hasActiveTask(userID) {
			text = mutationBusyText()
		} else {
			text = h.archiveCurrentSession(ctx, userID)
		}
	case actionConfirmArchiveItem:
		if h.hasActiveTask(userID) {
			text = mutationBusyText()
		} else {
			text = h.confirmArchiveSession(ctx, userID, option)
		}
	case actionArchiveItem:
		if h.hasActiveTask(userID) {
			text = mutationBusyText()
		} else {
			text = h.archiveSession(ctx, userID, option.Value)
		}
	case actionPickArchivedSession:
		text = h.openSessionPicker(ctx, userID, true, "")
	case actionRestoreSession:
		if h.hasActiveTask(userID) {
			text = mutationBusyText()
		} else {
			text = h.restoreSession(ctx, userID, option.Value)
		}
	case actionForkThread:
		if h.hasActiveTask(userID) {
			text = mutationBusyText()
		} else {
			text = h.forkCurrentThread(ctx, userID)
		}
	case actionToggleThreadPin:
		text = h.toggleCurrentThreadPin(ctx, userID, option.Value == "true")
	case actionCompactThread:
		if h.hasActiveTask(userID) {
			text = mutationBusyText()
		} else {
			text = h.compactCurrentThread(ctx, userID)
		}
	case actionPromptThreadGoal:
		text = h.promptCurrentThreadGoal(userID)
	case actionClearThreadGoal:
		text = h.clearCurrentThreadGoal(ctx, userID)
	case actionReviewThread:
		if h.hasActiveTask(userID) {
			text = mutationBusyText()
		} else {
			text = h.reviewCurrentThread(ctx, userID)
		}
	case actionCodexCapabilities:
		text = h.openCodexCapabilities(ctx, userID)
	case actionConfirmDeleteThread:
		text = h.confirmDeleteCurrentThread(ctx, userID)
	case actionDeleteThread:
		if h.hasActiveTask(userID) {
			text = mutationBusyText()
		} else {
			text = h.deleteCurrentThread(ctx, userID)
		}
	case actionThreadModels:
		text = h.openThreadModels(ctx, userID)
	case actionSelectThreadModel:
		text = h.selectThreadModel(ctx, userID, option.Value)
	case actionThreadEfforts:
		text = h.openThreadEfforts(ctx, userID, option.Value)
	case actionSelectThreadEffort:
		text = h.selectThreadEffort(ctx, userID, option.Query, option.Value)
	default:
		return invalidControlAction(option.Action, DomainSession)
	}
	return controlTextResult(option.Action, DomainSession, text)
}

func (h *Handler) dispatchSessionIntent(ctx context.Context, userID string, resolved ResolvedIntent, argument string) ActionResult {
	var text string
	switch resolved.Definition.ID {
	case IntentSessionCenter:
		text = h.openSessionMenu(ctx, userID)
	case IntentSessionSelect:
		if argument == "" {
			text = h.openSessionBrowser(ctx, userID, false, "")
		} else {
			text = h.openSessionPicker(ctx, userID, false, argument)
		}
	case IntentSessionSearch:
		if argument == "" {
			text = h.promptSessionSearch(userID)
		} else {
			text = h.openSessionBrowser(ctx, userID, false, argument)
		}
	case IntentSessionCurrent:
		text = h.currentSessionDetail(ctx, userID)
	case IntentSessionRestore:
		text = h.openSessionPicker(ctx, userID, true, argument)
	case IntentSessionNew:
		if argument == "" {
			text = h.promptNewSessionName(userID)
		} else {
			text = h.createSession(ctx, userID, argument)
		}
	case IntentSessionRename:
		if argument == "" {
			text = h.promptRenameSession(userID)
		} else {
			text = h.renameSession(ctx, userID, argument)
		}
	case IntentSessionArchive:
		text = h.confirmArchiveCurrent(ctx, userID)
	case IntentThreadFork:
		text = h.forkCurrentThread(ctx, userID)
	case IntentThreadPin:
		text = h.toggleCurrentThreadPin(ctx, userID, true)
	case IntentThreadCompact:
		text = h.compactCurrentThread(ctx, userID)
	case IntentThreadGoal:
		if argument == "" {
			text = h.promptCurrentThreadGoal(userID)
		} else {
			text = h.setCurrentThreadGoal(ctx, userID, argument)
		}
	case IntentThreadGoalClear:
		text = h.clearCurrentThreadGoal(ctx, userID)
	case IntentThreadReview:
		text = h.reviewCurrentThread(ctx, userID)
	case IntentThreadDelete:
		text = h.confirmDeleteCurrentThread(ctx, userID)
	case IntentThreadModels:
		text = h.openThreadModels(ctx, userID)
	case IntentThreadEffort:
		text = h.openThreadEfforts(ctx, userID, "")
	case IntentTurnSteer:
		text = h.steerCurrentTurn(ctx, userID, argument)
	default:
		return invalidIntentResult(resolved)
	}
	return intentTextResult(resolved, text)
}
