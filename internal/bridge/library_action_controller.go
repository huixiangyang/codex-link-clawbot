package bridge

import "github.com/huixiangyang/codex-link-clawbot/internal/control"

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
		return h.resendDelivery(userID, option.Value, option.Page).withIdentity(string(option.Action), control.DomainDelivery)
	default:
		return invalidControlAction(option.Action, control.DomainDelivery)
	}
	return controlTextResult(option.Action, control.DomainDelivery, text)
}

func (h *Handler) dispatchDeliveryIntent(userID string, resolved control.Resolved) ActionResult {
	switch resolved.Definition.ID {
	case control.IntentDeliveryBox:
		return intentTextResult(resolved, h.openDeliveryBox(userID))
	default:
		return invalidIntentResult(resolved)
	}
}
