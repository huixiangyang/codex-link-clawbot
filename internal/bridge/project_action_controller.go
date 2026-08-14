package bridge

import (
	"context"

	"github.com/huixiangyang/codex-link-clawbot/internal/control"
)

func (h *Handler) executeProjectControlAction(ctx context.Context, userID string, option controlOption) ActionResult {
	var text string
	switch option.Action {
	case actionProjectCenter:
		text = h.openProjectCenter(ctx, userID)
	case actionSelectProject:
		text = h.selectProject(userID, option.Value)
	default:
		return invalidControlAction(option.Action, control.DomainProject)
	}
	return controlTextResult(option.Action, control.DomainProject, text)
}

func (h *Handler) dispatchProjectIntent(ctx context.Context, userID string, resolved control.Resolved, argument string) ActionResult {
	var text string
	switch resolved.Definition.ID {
	case control.IntentProjectCenter:
		text = h.openProjectCenter(ctx, userID)
	case control.IntentProjectSelect:
		if argument == "" {
			text = h.openProjectCenter(ctx, userID)
		} else {
			text = h.selectProject(userID, argument)
		}
	default:
		return invalidIntentResult(resolved)
	}
	return intentTextResult(resolved, text)
}
