package messaging

import (
	"context"
	"fmt"
	"strings"

	"github.com/huixiangyang/weclaw/internal/codex"
	"github.com/huixiangyang/weclaw/internal/session"
)

func (h *Handler) advancedThreadContext() (codex.ThreadClient, codex.AdvancedThreadClient, error) {
	threadClient, err := h.sessionContext()
	if err != nil {
		return nil, nil, err
	}
	advanced, ok := h.codex.(codex.AdvancedThreadClient)
	if !ok {
		return nil, nil, fmt.Errorf("Codex 线程高级控制面不可用")
	}
	return threadClient, advanced, nil
}

func (h *Handler) forkCurrentThread(ctx context.Context, userID string) string {
	return h.withRuntimeMutation(func() string {
		threadClient, advanced, err := h.advancedThreadContext()
		if err != nil {
			return err.Error()
		}
		thread, err := h.sessions.ForkCurrent(ctx, userID, threadClient, advanced)
		if err != nil {
			return formatSessionError(err)
		}
		return h.sessionSuccess(userID, "已从当前历史分叉并切换到新线程。", thread)
	})
}

func (h *Handler) toggleCurrentThreadPin(ctx context.Context, userID string, pinned bool) string {
	_, advanced, err := h.advancedThreadContext()
	if err != nil {
		return err.Error()
	}
	thread, err := h.sessions.PinCurrent(ctx, userID, advanced, pinned)
	if err != nil {
		return formatSessionError(err)
	}
	state := "已取消置顶"
	if pinned {
		state = "已置顶"
	}
	return state + "当前线程。\n" + formatThreadIdentity(thread)
}

func (h *Handler) compactCurrentThread(ctx context.Context, userID string) string {
	return h.withRuntimeMutation(func() string {
		_, advanced, err := h.advancedThreadContext()
		if err != nil {
			return err.Error()
		}
		if err := h.sessions.CompactCurrent(ctx, userID, advanced); err != nil {
			return formatSessionError(err)
		}
		return "已启动当前线程的上下文压缩。Codex 会保留关键决策并释放上下文空间。"
	})
}

func (h *Handler) promptCurrentThreadGoal(userID string) string {
	if h.sessions == nil {
		return "Codex 线程管理器不可用。"
	}
	prompt := "设置线程目标\n\n发送目标正文，最多 4,000 字；新目标会替换旧目标并重置用量统计。回复 0 返回。"
	if !h.storeInput(userID, viewSessionCurrent, controlThreadGoal, actionSessionMenu) {
		return controlStateFailureResult().Text
	}
	return prompt
}

func (h *Handler) setCurrentThreadGoal(ctx context.Context, userID, objective string) string {
	_, advanced, err := h.advancedThreadContext()
	if err != nil {
		return err.Error()
	}
	goal, err := h.sessions.SetCurrentGoal(ctx, userID, advanced, objective)
	if err != nil {
		return formatSessionError(err)
	}
	return strings.Join([]string{
		"线程目标已设置",
		"状态：" + displayGoalStatus(goal.Status),
		"目标：" + normalizeSessionLine(goal.Objective, 240),
	}, "\n")
}

func (h *Handler) clearCurrentThreadGoal(ctx context.Context, userID string) string {
	_, advanced, err := h.advancedThreadContext()
	if err != nil {
		return err.Error()
	}
	if err := h.sessions.ClearCurrentGoal(ctx, userID, advanced); err != nil {
		return formatSessionError(err)
	}
	return "当前线程目标已清除。"
}

func (h *Handler) reviewCurrentThread(ctx context.Context, userID string) string {
	return h.withRuntimeMutation(func() string {
		_, advanced, err := h.advancedThreadContext()
		if err != nil {
			return err.Error()
		}
		review, err := h.sessions.ReviewCurrent(ctx, userID, advanced, codex.ReviewTarget{Type: "uncommittedChanges"}, nil)
		if err != nil {
			return "代码审查失败：" + err.Error()
		}
		return "代码审查\n\n" + strings.TrimSpace(review)
	})
}

func (h *Handler) openCodexCapabilities(ctx context.Context, userID string) string {
	capabilityClient, ok := h.codex.(codex.CapabilityClient)
	if !ok || h.projects == nil {
		return "Codex 能力目录当前不可用。"
	}
	entry := h.projects.Current(userID)
	capabilities, err := capabilityClient.InspectProject(ctx, entry.Root)
	if err != nil {
		return "读取 Codex 能力目录失败：" + err.Error()
	}
	enabledSkills := make([]string, 0, len(capabilities.Skills))
	for _, skill := range capabilities.Skills {
		if !skill.Enabled {
			continue
		}
		name := strings.TrimSpace(skill.Interface.DisplayName)
		if name == "" {
			name = skill.Name
		}
		enabledSkills = append(enabledSkills, name)
	}
	displayedSkills := enabledSkills
	if len(displayedSkills) > 5 {
		displayedSkills = displayedSkills[:5]
	}
	lines := []string{
		"Codex 能力",
		"",
		"来源：Codex 应用服务",
		"工作入口：" + entry.Name,
		fmt.Sprintf("技能：%d 个启用", len(enabledSkills)),
		fmt.Sprintf("外部工具连接：%d / %d 就绪", capabilities.MCPReady, capabilities.MCPServers),
	}
	if len(displayedSkills) > 0 {
		lines = append(lines, "技能列表："+strings.Join(displayedSkills, " · "))
	}
	options := []controlOption{
		{Label: "模型与推理", Action: actionThreadModels},
		{Label: "当前线程", Action: actionCurrentSession},
		{Label: "返回 Codex 工作台", Action: actionSessionMenu},
	}
	lines = append(lines, "", renderControlOptions(options))
	if !h.storeChoice(userID, viewSessionCenter, options, actionSessionMenu) {
		return controlStateFailureResult().Text
	}
	return strings.Join(lines, "\n") + "\n\n回复数字操作，0 返回 Codex 工作台。"
}

func (h *Handler) steerCurrentTurn(ctx context.Context, userID, instruction string) string {
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return "追加指令不能为空。示例：追加指令 先修复失败测试。"
	}
	_, advanced, err := h.advancedThreadContext()
	if err != nil {
		return err.Error()
	}
	if err := h.sessions.SteerCurrent(ctx, userID, advanced, codex.ChatRequest{Text: instruction}); err != nil {
		return "无法追加到当前 Codex 轮次：" + err.Error()
	}
	return "指令已追加到当前进行中的 Codex 轮次，不会创建新轮次。"
}

func (h *Handler) confirmDeleteCurrentThread(ctx context.Context, userID string) string {
	threadClient, _, err := h.advancedThreadContext()
	if err != nil {
		return err.Error()
	}
	current, err := h.sessions.Current(ctx, userID, threadClient)
	if err != nil {
		return formatSessionError(err)
	}
	options := []controlOption{{Label: "永久删除这个线程", Action: actionDeleteThread}}
	prompt := strings.Join([]string{
		"准备永久删除线程：" + threadTitle(current.Info),
		"",
		"Codex 将同时删除它的持久历史和所有派生线程，此操作不可恢复。",
		"",
		renderControlOptions(options),
	}, "\n")
	if !h.storeChoice(userID, viewSessionArchive, options, actionSessionMenu) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复 1 确认，0 返回。"
}

func (h *Handler) deleteCurrentThread(ctx context.Context, userID string) string {
	return h.withRuntimeMutation(func() string {
		_, advanced, err := h.advancedThreadContext()
		if err != nil {
			return err.Error()
		}
		if err := h.sessions.DeleteCurrent(ctx, userID, advanced); err != nil {
			return formatSessionError(err)
		}
		return "当前线程及其派生历史已永久删除。下一条普通消息会在当前 WeClaw 项目入口中新建 Codex 线程。"
	})
}

func (h *Handler) openThreadModels(ctx context.Context, userID string) string {
	capabilities, ok := h.codex.(codex.CapabilityClient)
	if !ok || h.sessions == nil {
		return "Codex 模型目录不可用。"
	}
	models, err := capabilities.ListModels(ctx)
	if err != nil {
		return "读取 Codex 模型目录失败：" + err.Error()
	}
	settings, err := h.sessions.CurrentSettings(userID)
	if err != nil {
		return formatSessionError(err)
	}
	options := make([]controlOption, 0, len(models))
	for _, model := range models {
		label := model.DisplayName
		if label == "" {
			label = model.ID
		}
		if model.ID == settings.Model || settings.Model == "" && model.IsDefault {
			label += " · 当前"
		}
		options = append(options, controlOption{Label: label, Action: actionSelectThreadModel, Value: model.ID})
	}
	if len(options) == 0 {
		return "Codex 没有返回可选择的模型。"
	}
	prompt := "线程模型\n\n模型设置属于当前 Codex 线程，并从下一次 Codex 轮次开始生效。\n\n" + renderControlOptions(options)
	if !h.storeChoice(userID, viewSessionCurrent, options, actionSessionMenu) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字选择，0 返回。"
}

func (h *Handler) selectThreadModel(ctx context.Context, userID, modelID string) string {
	model, ok, err := h.findModel(ctx, modelID)
	if err != nil {
		return "读取 Codex 模型目录失败：" + err.Error()
	}
	if !ok {
		return "这个 Codex 模型已不可用，请刷新模型目录。"
	}
	settings := session.ThreadSettings{Model: model.ID, Effort: model.DefaultReasoningEffort}
	if err := h.sessions.SetCurrentSettings(userID, settings); err != nil {
		return formatSessionError(err)
	}
	if len(model.SupportedReasoningEfforts) > 1 {
		return h.openThreadEfforts(ctx, userID, model.ID)
	}
	return fmt.Sprintf("当前线程已切换到 %s。推理强度：%s。", modelLabel(model), displayEffort(settings.Effort))
}

func (h *Handler) openThreadEfforts(ctx context.Context, userID, modelID string) string {
	settings, err := h.sessions.CurrentSettings(userID)
	if err != nil {
		return formatSessionError(err)
	}
	if strings.TrimSpace(modelID) == "" {
		modelID = settings.Model
	}
	model, ok, err := h.findModel(ctx, modelID)
	if err != nil {
		return "读取 Codex 模型目录失败：" + err.Error()
	}
	if !ok {
		return "当前线程的模型已不在 Codex 模型目录中。"
	}
	options := make([]controlOption, 0, len(model.SupportedReasoningEfforts))
	for _, effort := range model.SupportedReasoningEfforts {
		label := displayEffort(effort.Effort)
		if effort.Effort == settings.Effort {
			label += " · 当前"
		}
		options = append(options, controlOption{Label: label, Action: actionSelectThreadEffort, Query: model.ID, Value: effort.Effort})
	}
	if len(options) == 0 {
		return "这个模型没有公开可选的推理强度。"
	}
	prompt := "推理强度\n\n模型：" + modelLabel(model) + "\n\n" + renderControlOptions(options)
	if !h.storeChoice(userID, viewSessionCurrent, options, actionThreadModels) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字选择，0 返回模型目录。"
}

func (h *Handler) selectThreadEffort(ctx context.Context, userID, modelID, effortID string) string {
	model, ok, err := h.findModel(ctx, modelID)
	if err != nil || !ok {
		return "Codex 模型目录已变化，请重新选择模型。"
	}
	valid := false
	for _, effort := range model.SupportedReasoningEfforts {
		if effort.Effort == effortID {
			valid = true
			break
		}
	}
	if !valid {
		return "这个推理强度不受当前模型支持。"
	}
	if err := h.sessions.SetCurrentSettings(userID, session.ThreadSettings{Model: model.ID, Effort: effortID}); err != nil {
		return formatSessionError(err)
	}
	return fmt.Sprintf("当前线程设置已更新。\n模型：%s\n推理强度：%s", modelLabel(model), displayEffort(effortID))
}

func (h *Handler) findModel(ctx context.Context, modelID string) (codex.ModelInfo, bool, error) {
	capabilities, ok := h.codex.(codex.CapabilityClient)
	if !ok {
		return codex.ModelInfo{}, false, fmt.Errorf("模型目录不可用")
	}
	models, err := capabilities.ListModels(ctx)
	if err != nil {
		return codex.ModelInfo{}, false, err
	}
	for _, model := range models {
		if model.ID == modelID || model.Model == modelID || modelID == "" && model.IsDefault {
			return model, true, nil
		}
	}
	return codex.ModelInfo{}, false, nil
}

func modelLabel(model codex.ModelInfo) string {
	if strings.TrimSpace(model.DisplayName) != "" {
		return model.DisplayName
	}
	return model.ID
}

func displayEffort(effort string) string {
	switch effort {
	case "none":
		return "无"
	case "minimal":
		return "极低"
	case "low":
		return "低"
	case "medium":
		return "中"
	case "high":
		return "高"
	case "xhigh":
		return "极高"
	case "max":
		return "顶级"
	case "ultra":
		return "最高"
	default:
		if effort == "" {
			return "模型默认"
		}
		return "未知"
	}
}

func displayGoalStatus(status string) string {
	switch status {
	case "active":
		return "进行中"
	case "paused":
		return "已暂停"
	case "blocked":
		return "受阻"
	case "usageLimited":
		return "用量受限"
	case "budgetLimited":
		return "预算用尽"
	case "complete":
		return "已完成"
	default:
		return "未知"
	}
}
