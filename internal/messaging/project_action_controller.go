package messaging

import "context"

func (h *Handler) executeProjectControlAction(ctx context.Context, userID string, option controlOption) ActionResult {
	var text string
	switch option.Action {
	case actionProjectCenter:
		text = h.openProjectCenter(ctx, userID)
	case actionSelectProject:
		text = h.selectProject(userID, option.Value)
	default:
		return invalidControlAction(option.Action, DomainProject)
	}
	return controlTextResult(option.Action, DomainProject, text)
}

func (h *Handler) dispatchProjectIntent(ctx context.Context, userID string, resolved ResolvedIntent, argument string) ActionResult {
	var text string
	switch resolved.Definition.ID {
	case IntentProjectCenter:
		text = h.openProjectCenter(ctx, userID)
	case IntentProjectSelect:
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
