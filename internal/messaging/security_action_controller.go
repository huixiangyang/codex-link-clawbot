package messaging

func (h *Handler) executeSecurityControlAction(userID string, option controlOption) ActionResult {
	switch option.Action {
	case actionConfirmRemoteLock:
		return controlTextResult(option.Action, DomainSecurity, h.confirmRemoteLock(userID))
	case actionRemoteLock:
		return controlTextResult(option.Action, DomainSecurity, h.lockRemote(userID))
	default:
		return invalidControlAction(option.Action, DomainSecurity)
	}
}

func (h *Handler) dispatchSecurityIntent(userID string, resolved ResolvedIntent) ActionResult {
	switch resolved.Definition.ID {
	case IntentRemoteLock:
		return intentTextResult(resolved, h.lockRemote(userID))
	default:
		return invalidIntentResult(resolved)
	}
}
