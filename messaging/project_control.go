package messaging

import (
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/huixiangyang/weclaw/ilink"
	"github.com/huixiangyang/weclaw/project"
	"github.com/huixiangyang/weclaw/session"
)

func (h *Handler) openProjectCenter(userID string) string {
	if h.projects == nil {
		return "项目中心未初始化。"
	}
	current := h.projects.Current(userID)
	workflowCount := 0
	if h.workflows != nil {
		workflowCount = len(h.workflows.List(userID, current.ID))
	}
	stats := sessionStats(h, userID)
	options := make([]controlOption, 0, len(h.projects.List())+1)
	options = append(options, controlOption{Label: "快捷任务", Action: actionProjectQuickTasks, Query: current.ID, Page: 1})
	for _, definition := range h.projects.List() {
		label := definition.Name
		if definition.ID == current.ID {
			label += " · 当前"
		}
		options = append(options, controlOption{Label: label, Action: actionSelectProject, Value: definition.ID})
	}
	lines := []string{
		"项目中心",
		"",
		"当前：" + current.Name,
		"标识：" + current.ID,
		"目录：" + current.Root,
		fmt.Sprintf("会话：%d 个可用 · %d 个归档", stats.Active, stats.Archived),
	}
	if current.ServiceName != "" {
		lines = append(lines, "服务："+current.ServiceName)
	}
	if current.HealthURL != "" {
		lines = append(lines, "健康检查：已配置")
	}
	lines = append(lines, fmt.Sprintf("快捷任务：%d 项", workflowCount))
	lines = append(lines, "", renderControlOptions(options))
	prompt := strings.Join(lines, "\n")
	if !h.storeChoice(userID, viewProjectCenter, options, actionMain) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字切换项目，0 返回。"
}

func (h *Handler) openProjectQuickTasks(userID string) string {
	if h.projects == nil {
		return "快捷任务当前不可用。"
	}
	return h.openWorkflowCenter(userID, h.projects.Current(userID).ID, 1)
}

func (h *Handler) runProjectQuickTask(userID, projectID, taskID string) ActionResult {
	if h.projects == nil || h.workflows == nil {
		return newActionResult(string(actionRunQuickTask), DomainProject, "快捷任务当前不可用。")
	}
	if _, exists := h.projects.Get(projectID); !exists {
		return newActionResult(string(actionRunQuickTask), DomainProject, "这个项目已经不可用。发送“项目”刷新列表。")
	}
	definition, exists := h.workflows.Find(userID, projectID, taskID)
	if !exists {
		return newActionResult(string(actionRunQuickTask), DomainProject, "快捷任务已经变化。发送“快捷任务”刷新列表。")
	}
	if len(definition.Slots) > 0 {
		status, err := h.workflows.StartRun(userID, projectID, taskID)
		if err != nil {
			return newActionResult(string(actionRunQuickTask), DomainProject, workflowUnavailableText())
		}
		h.deleteControlState(userID)
		return newActionResult(string(actionRunQuickTask), DomainProject, workflowParameterPrompt(status))
	}
	// 菜单请求由当前微信消息继续执行，避免要求用户再复制或确认提示词。
	h.deleteControlState(userID)
	return effectActionResult(string(actionRunQuickTask), DomainProject, "", EffectEnqueuePrompt, definition.PromptTemplate).withProjectID(projectID)
}

func (h *Handler) selectProject(userID, reference string) string {
	if h.projects == nil {
		return "项目中心未初始化。"
	}
	definition, err := h.projects.Resolve(reference)
	if err != nil {
		if errors.Is(err, project.ErrUnknownProject) {
			return "没有找到唯一匹配的项目。发送“项目”查看可选项目。"
		}
		return fmt.Sprintf("项目选择失败：%v", err)
	}
	selected, err := h.projects.Select(userID, definition.ID)
	if err != nil {
		return fmt.Sprintf("项目选择失败：%v", err)
	}
	// 项目选择只更新界面状态；Codex cwd 只能由串行 Coordinator 在领取任务时设置。
	log.Printf("[project] selected project=%s for %s", selected.ID, ilink.LogLabel(userID))
	stats := sessionStats(h, userID)
	currentSession := "未创建"
	if stats.HasCurrent {
		currentSession = "已有当前会话"
	}
	options := []controlOption{
		{Label: "进入会话中心", Action: actionSessionMenu},
		{Label: "返回项目中心", Action: actionProjectCenter},
	}
	prompt := strings.Join([]string{
		"项目已切换",
		"",
		"当前：" + selected.Name,
		"目录：" + selected.Root,
		"会话：" + currentSession,
		"",
		renderControlOptions(options),
	}, "\n")
	if !h.storeChoice(userID, viewProjectResult, options, actionProjectCenter) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n下一条内容将在这个项目中执行；回复数字继续，0 返回。"
}

func sessionStats(h *Handler, userID string) session.Stats {
	if h.sessions == nil {
		return session.Stats{}
	}
	return h.sessions.Stats(userID)
}
