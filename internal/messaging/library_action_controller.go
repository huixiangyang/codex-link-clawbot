package messaging

func (h *Handler) executeDeliveryControlAction(userID string, option controlOption) ActionResult {
	var text string
	switch option.Action {
	case actionDeliveryBox:
		text = h.openDeliveryBox(userID)
	case actionDeliveryPage:
		text = h.openDeliveryPage(userID, option.Page)
	case actionDeliveryDetail:
		text = h.openDeliveryDetail(userID, option.Value, option.Page)
	case actionResendDelivery:
		return h.resendDelivery(userID, option.Value, option.Page).withIdentity(string(option.Action), DomainDelivery)
	default:
		return invalidControlAction(option.Action, DomainDelivery)
	}
	return controlTextResult(option.Action, DomainDelivery, text)
}

func (h *Handler) dispatchDeliveryIntent(userID string, resolved ResolvedIntent) ActionResult {
	switch resolved.Definition.ID {
	case IntentDeliveryBox:
		return intentTextResult(resolved, h.openDeliveryBox(userID))
	default:
		return invalidIntentResult(resolved)
	}
}
