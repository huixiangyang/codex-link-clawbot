package bridge

import "github.com/huixiangyang/codex-link-clawbot/internal/control"

func (h *Handler) executeSecurityControlAction(userID string, option controlOption) ActionResult {
	switch option.Action {
	case actionConfirmRemoteLock:
		return controlTextResult(option.Action, control.DomainSecurity, h.confirmRemoteLock(userID))
	case actionRemoteLock:
		return controlTextResult(option.Action, control.DomainSecurity, h.lockRemote(userID))
	default:
		return invalidControlAction(option.Action, control.DomainSecurity)
	}
}

func (h *Handler) dispatchSecurityIntent(userID string, resolved control.Resolved) ActionResult {
	switch resolved.Definition.ID {
	case control.IntentRemoteLock:
		return intentTextResult(resolved, h.lockRemote(userID))
	default:
		return invalidIntentResult(resolved)
	}
}
