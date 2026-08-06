package messaging

import "context"

func (h *Handler) executeProjectControlAction(ctx context.Context, userID string, option controlOption) ActionResult {
	var text string
	switch option.Action {
	case actionProjectCenter:
		text = h.openProjectCenter(ctx, userID)
	case actionSelectProject:
		text = h.selectProject(userID, option.Value)
	case actionProjectQuickTasks:
		projectID := option.Query
		if projectID == "" && h.projects != nil {
			projectID = h.projects.Current(userID).ID
		}
		if option.AutoUse {
			text = h.openWorkflowRunPicker(userID, projectID, option.Page)
		} else {
			text = h.openWorkflowCenter(userID, projectID, option.Page)
		}
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
	case actionPromptWorkflowSave:
		text = h.promptWorkflowSaveFromTask(userID, option)
	case actionSaveRecentWorkflow:
		task, exists := h.latestSuccessfulTask(userID, true)
		if !exists {
			text = "最近没有仍可保存的成功纯文字请求。"
		} else {
			text = h.promptWorkflowSaveFromTask(userID, controlOption{
				Action: actionPromptWorkflowSave, Value: task.ID, Query: task.ProjectID, Page: 1,
			})
		}
	case actionRunQuickTask:
		return h.runProjectQuickTask(userID, option.Query, option.Value).withIdentity(string(option.Action), DomainProject)
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
	case IntentProjectQuickTasks:
		text = h.openProjectQuickTasks(userID)
	case IntentWorkflowNew:
		if h.projects == nil {
			text = "提示词模板当前不可用。"
		} else {
			text = h.promptWorkflowCreate(userID, h.projects.Current(userID).ID, 1)
		}
	case IntentWorkflowSaveLast:
		task, ok := h.latestSuccessfulTask(userID, true)
		if !ok {
			text = "最近没有仍可保存的成功纯文字请求。"
		} else {
			text = h.promptWorkflowSaveFromTask(userID, controlOption{
				Action: actionPromptWorkflowSave, Value: task.ID, Query: task.ProjectID, Page: 1,
			})
		}
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
