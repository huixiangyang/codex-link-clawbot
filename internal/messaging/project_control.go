package messaging

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/huixiangyang/weclaw/internal/ilink"
	"github.com/huixiangyang/weclaw/internal/project"
	"github.com/huixiangyang/weclaw/internal/session"
)

func (h *Handler) openProjectCenter(_ context.Context, userID string) string {
	if h.projects == nil {
		return "WeClaw 项目入口未初始化。"
	}
	current := h.projects.Current(userID)
	entries := h.projects.List()
	options := make([]controlOption, 0, len(entries)+1)
	for _, definition := range entries {
		label := definition.Name
		if definition.ID == current.ID {
			label += " · 当前"
		}
		options = append(options, controlOption{Label: label, Action: actionSelectProject, Value: definition.ID})
	}
	options = append(options, controlOption{Label: "有效配置状态", Action: actionConfigurationStatus})
	lines := []string{
		"WeClaw 项目入口",
		"",
		"边界：这里只管理 Codex 可以进入的受信任本机目录，不管理 Codex 线程和能力。",
		"当前：" + current.Name,
		"标识：" + current.ID,
		"目录：" + current.Root,
		fmt.Sprintf("入口数量：%d", len(entries)),
	}
	if current.ServiceName != "" {
		lines = append(lines, "服务检查：已配置")
	}
	if current.HealthURL != "" {
		lines = append(lines, "健康检查：已配置")
	}
	lines = append(lines, "", renderControlOptions(options))
	prompt := strings.Join(lines, "\n")
	if !h.storeChoice(userID, viewProjectCenter, options, actionSettingsCenter) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字管理或切换 WeClaw 项目入口，0 返回。"
}

func (h *Handler) openProjectQuickTasks(userID string) string {
	if h.projects == nil {
		return "提示词模板当前不可用。"
	}
	return h.openWorkflowCenter(userID, h.projects.Current(userID).ID, 1)
}

func (h *Handler) runProjectQuickTask(userID, projectID, taskID string) ActionResult {
	if h.projects == nil || h.workflows == nil {
		return newActionResult(string(actionRunQuickTask), DomainProject, "提示词模板当前不可用。")
	}
	if _, exists := h.projects.Get(projectID); !exists {
		return newActionResult(string(actionRunQuickTask), DomainProject, "这个 WeClaw 项目入口已经不可用。发送“项目”刷新列表。")
	}
	definition, exists := h.workflows.Find(userID, projectID, taskID)
	if !exists {
		return newActionResult(string(actionRunQuickTask), DomainProject, "提示词模板已经变化。发送“提示词模板”刷新列表。")
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
		return "WeClaw 项目入口未初始化。"
	}
	definition, err := h.projects.Resolve(reference)
	if err != nil {
		if errors.Is(err, project.ErrUnknownProject) {
			return "没有找到唯一匹配的 WeClaw 项目入口。发送“项目”查看可选目录。"
		}
		return fmt.Sprintf("WeClaw 项目入口选择失败：%v", err)
	}
	selected, err := h.projects.Select(userID, definition.ID)
	if err != nil {
		return fmt.Sprintf("WeClaw 项目入口选择失败：%v", err)
	}
	// WeClaw 项目入口只更新受信任目录选择；Codex 工作目录由串行协调器领取请求后设置。
	log.Printf("[project] selected project=%s for %s", selected.ID, ilink.LogLabel(userID))
	stats := sessionStats(h, userID)
	currentSession := "未创建"
	if stats.HasCurrent {
		currentSession = "已有当前线程"
	}
	options := []controlOption{
		{Label: "进入 Codex 线程", Action: actionSessionMenu},
		{Label: "返回项目入口", Action: actionProjectCenter},
	}
	prompt := strings.Join([]string{
		"WeClaw 项目入口已切换",
		"",
		"当前：" + selected.Name,
		"目录：" + selected.Root,
		"线程：" + currentSession,
		"",
		renderControlOptions(options),
	}, "\n")
	if !h.storeChoice(userID, viewProjectResult, options, actionProjectCenter) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n下一条内容会先进入 WeClaw 请求队列，再由 Codex 在这个工作目录中执行；回复数字继续，0 返回。"
}

func sessionStats(h *Handler, userID string) session.Stats {
	if h.sessions == nil {
		return session.Stats{}
	}
	return h.sessions.Stats(userID)
}
