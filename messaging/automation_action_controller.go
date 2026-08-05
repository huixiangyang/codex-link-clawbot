package messaging

import "context"

func (h *Handler) executeAutomationControlAction(ctx context.Context, userID string, option controlOption) ActionResult {
	var text string
	switch option.Action {
	case actionAutomations:
		text = h.openAutomations(userID, option.Page)
	case actionAutomation:
		text = h.openAutomation(userID, option.Value, option.Page)
	case actionRunAutomation:
		text = h.runAutomation(ctx, userID, option.Value, option.Page)
	case actionVoiceBriefing:
		return h.requestVoiceBriefing(userID).withIdentity(string(option.Action), DomainAutomation)
	default:
		return invalidControlAction(option.Action, DomainAutomation)
	}
	return controlTextResult(option.Action, DomainAutomation, text)
}

func (h *Handler) dispatchAutomationIntent(userID string, resolved ResolvedIntent) ActionResult {
	switch resolved.Definition.ID {
	case IntentAutomationCenter:
		return intentTextResult(resolved, h.openAutomations(userID, 1))
	case IntentVoiceBriefing:
		return h.requestVoiceBriefing(userID).withIdentity(string(resolved.Definition.ID), DomainAutomation)
	default:
		return invalidIntentResult(resolved)
	}
}
