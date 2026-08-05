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
	stats := sessionStats(h, userID)
	options := make([]controlOption, 0, len(h.projects.List())+1)
	if len(current.QuickTasks) > 0 {
		options = append(options, controlOption{Label: "快捷任务", Action: actionProjectQuickTasks})
	}
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
	if len(current.QuickTasks) > 0 {
		lines = append(lines, fmt.Sprintf("快捷任务：%d 项", len(current.QuickTasks)))
	}
	lines = append(lines, "", renderControlOptions(options))
	prompt := strings.Join(lines, "\n")
	h.storeChoice(userID, prompt, options, actionMain)
	return prompt + "\n\n回复数字切换项目，0 返回。"
}

func (h *Handler) openProjectQuickTasks(userID string) string {
	if h.projects == nil {
		return "项目中心未初始化。"
	}
	current := h.projects.Current(userID)
	if len(current.QuickTasks) == 0 {
		return "当前项目没有配置快捷任务。"
	}
	options := make([]controlOption, 0, len(current.QuickTasks))
	for _, task := range current.QuickTasks {
		options = append(options, controlOption{Label: task.Name, Action: actionRunQuickTask, Value: task.ID})
	}
	prompt := "快捷任务\n\n项目：" + current.Name + "\n选择后立即交给 Codex 执行。\n\n" + renderControlOptions(options)
	h.storeChoice(userID, prompt, options, actionProjectCenter)
	return prompt + "\n\n回复数字执行，0 返回项目中心。"
}

func (h *Handler) runProjectQuickTask(userID, taskID string) string {
	if h.projects == nil {
		return "项目中心未初始化。"
	}
	current := h.projects.Current(userID)
	task, exists := h.projects.QuickTask(current.ID, taskID)
	if !exists {
		return "快捷任务已经变化。发送“快捷任务”刷新列表。"
	}
	// 菜单请求由当前微信消息继续执行，避免要求用户再复制或确认提示词。
	h.controlDispatches.Store(userID, task.Prompt)
	h.controlStates.Delete(userID)
	return ""
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
	h.storeChoice(userID, prompt, options, actionProjectCenter)
	return prompt + "\n\n下一条内容将在这个项目中执行；回复数字继续，0 返回。"
}

func sessionStats(h *Handler, userID string) session.Stats {
	if h.sessions == nil {
		return session.Stats{}
	}
	return h.sessions.Stats(userID)
}
