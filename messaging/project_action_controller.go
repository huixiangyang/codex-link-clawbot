package messaging

func (h *Handler) executeProjectControlAction(userID string, option controlOption) ActionResult {
	var text string
	switch option.Action {
	case actionProjectCenter:
		text = h.openProjectCenter(userID)
	case actionSelectProject:
		text = h.selectProject(userID, option.Value)
	case actionProjectQuickTasks:
		text = h.openProjectQuickTasks(userID)
	case actionRunQuickTask:
		return h.runProjectQuickTask(userID, option.Value).withIdentity(string(option.Action), DomainProject)
	default:
		return invalidControlAction(option.Action, DomainProject)
	}
	return controlTextResult(option.Action, DomainProject, text)
}

func (h *Handler) dispatchProjectIntent(userID string, resolved ResolvedIntent, argument string) ActionResult {
	var text string
	switch resolved.Definition.ID {
	case IntentProjectCenter:
		text = h.openProjectCenter(userID)
	case IntentProjectQuickTasks:
		text = h.openProjectQuickTasks(userID)
	case IntentProjectSelect:
		if argument == "" {
			text = h.openProjectCenter(userID)
		} else {
			text = h.selectProject(userID, argument)
		}
	default:
		return invalidIntentResult(resolved)
	}
	return intentTextResult(resolved, text)
}
