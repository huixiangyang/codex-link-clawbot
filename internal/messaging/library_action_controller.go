package messaging

func (h *Handler) executeLibraryControlAction(userID string, option controlOption) ActionResult {
	var text string
	switch option.Action {
	case actionLibraryCenter:
		text = h.openLibraryCenter(userID)
	case actionLibraryPage:
		text = h.openLibraryPage(userID, LibraryKind(option.Query), option.Page)
	case actionLibraryDetail:
		text = h.openLibraryDetail(userID, option.Value, LibraryKind(option.Query), option.Page)
	case actionResendDelivery:
		return h.resendDelivery(userID, option.Value, option.Page).withIdentity(string(option.Action), DomainLibrary)
	default:
		return invalidControlAction(option.Action, DomainLibrary)
	}
	return controlTextResult(option.Action, DomainLibrary, text)
}

func (h *Handler) dispatchLibraryIntent(userID string, resolved ResolvedIntent) ActionResult {
	switch resolved.Definition.ID {
	case IntentLibraryCenter:
		return intentTextResult(resolved, h.openLibraryCenter(userID))
	default:
		return invalidIntentResult(resolved)
	}
}
