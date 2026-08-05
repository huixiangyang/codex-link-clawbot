package messaging

func (h *Handler) executeProjectControlAction(userID string, option controlOption) ActionResult {
	var text string
	switch option.Action {
	case actionProjectCenter:
		text = h.openProjectCenter(userID)
	case actionSelectProject:
		text = h.selectProject(userID, option.Value)
	case actionProjectQuickTasks:
		projectID := option.Query
		if projectID == "" && h.projects != nil {
			projectID = h.projects.Current(userID).ID
		}
		text = h.openWorkflowCenter(userID, projectID, option.Page)
	case actionWorkflowDetail:
		text = h.openWorkflowDetail(userID, option)
	case actionPromptWorkflowCreate:
		text = h.promptWorkflowCreate(userID, option.Query, option.Page)
	case actionPromptWorkflowRename:
		text = h.promptWorkflowRename(userID, option)
	case actionPromptWorkflowEdit:
		text = h.promptWorkflowEdit(userID, option)
	case actionConfirmWorkflowDelete:
		text = h.confirmWorkflowDelete(userID, option)
	case actionWorkflowDelete:
		text = h.deleteWorkflow(userID, option.Query, option.Value, option.Page)
	case actionRunQuickTask:
		return h.runProjectQuickTask(userID, option.Query, option.Value).withIdentity(string(option.Action), DomainProject)
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
	case IntentWorkflowNew:
		if h.projects == nil {
			text = "快捷任务当前不可用。"
		} else {
			text = h.promptWorkflowCreate(userID, h.projects.Current(userID).ID, 1)
		}
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
