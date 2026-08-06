package messaging

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/huixiangyang/weclaw/internal/session"
)

type commandDirectorySection struct {
	Code     string
	Title    string
	Category controlOption
	Items    []controlOption
}

// openCommandDirectory 生成一张总览所需的稳定命令目录；运行状态只改变标签，不改变编号。
func (h *Handler) openCommandDirectory(ctx context.Context, userID string) string {
	currentSession := "未创建"
	if h.sessions != nil {
		if threadAgent, err := h.sessionContext(); err == nil {
			if current, currentErr := h.sessions.Current(ctx, userID, threadAgent); currentErr == nil {
				currentSession = threadTitle(current.Info)
			} else if !errors.Is(currentErr, session.ErrNoActive) {
				currentSession = "暂不可读"
			}
		}
	}

	projectID := ""
	projectName := "未配置"
	workflowCount := 0
	if h.projects != nil {
		current := h.projects.Current(userID)
		projectID = current.ID
		projectName = current.Name
		if h.workflows != nil {
			workflowCount = len(h.workflows.List(userID, current.ID))
		}
	}

	taskState := "空闲"
	queued := 0
	paused := false
	if h.tasks != nil {
		status := h.tasks.Status(userID)
		queued = status.Queued
		paused = status.Paused
		if status.Running > 0 {
			taskState = "运行中"
		} else if status.Delivering > 0 {
			taskState = "发送中"
		} else if status.Queued > 0 {
			taskState = "等待中"
		}
	}

	saveRecentLabel := "保存最近结果"
	if _, exists := h.latestSuccessfulTask(userID, true); !exists {
		saveRecentLabel += " · 暂无可保存内容"
	}
	queueToggle := controlOption{Code: "33", Label: "暂停队列", Action: actionQueuePause}
	if paused {
		queueToggle.Label = "继续队列 · 当前已暂停"
		queueToggle.Action = actionQueueResume
	}
	cancelLabel := "取消当前执行"
	if !h.hasActiveTask(userID) {
		cancelLabel += " · 当前无执行"
	}
	clearLabel := "清空等待请求"
	if queued == 0 {
		clearLabel += " · 当前为空"
	}

	preference := h.currentResponseMode(userID)
	style := h.currentVisualStyle(userID)

	voiceLabel := "语音简报"
	if h.voice == nil {
		voiceLabel += " · 当前不可用"
	}
	automationLabel := fmt.Sprintf("自动化中心 · %d 项", len(h.automationStatuses(userID)))
	lockLabel := "远程锁定"
	if h.remoteLock == nil || !h.remoteLock.Enabled() {
		lockLabel += " · 未配置"
	}

	sections := []commandDirectorySection{
		{
			Code: "1", Title: "Codex · 工作台",
			Category: controlOption{Code: "1", Label: "Codex · 工作台", Action: actionSessionMenu},
			Items: []controlOption{
				{Code: "11", Label: "当前线程", Action: actionCurrentSession},
				{Code: "12", Label: "新建线程", Action: actionPromptNewSession},
				{Code: "13", Label: "切换线程", Action: actionPickSession},
				{Code: "14", Label: "线程中心", Action: actionSessionMenu},
				{Code: "15", Label: "模型与推理", Action: actionThreadModels},
				{Code: "16", Label: "设置线程目标", Action: actionPromptThreadGoal},
				{Code: "17", Label: "审查未提交改动", Action: actionReviewThread},
				{Code: "18", Label: "Codex 能力", Action: actionCodexCapabilities},
			},
		},
		{
			Code: "2", Title: "WeClaw · 请求",
			Category: controlOption{Code: "2", Label: "WeClaw · 请求", Action: actionActivityPage, Page: 1},
			Items: []controlOption{
				{Code: "21", Label: "执行状态", Action: actionTaskStatus},
				{Code: "22", Label: "执行记录", Action: actionActivityPage, Page: 1},
				{Code: "23", Label: "最近结果", Action: actionRecentResult},
				{Code: "24", Label: cancelLabel, Action: actionConfirmCancelTask},
				{Code: "25", Label: queueToggle.Label, Action: queueToggle.Action},
			},
		},
		{
			Code: "3", Title: "WeClaw · 回复",
			Category: controlOption{Code: "3", Label: "WeClaw · 回复", Action: actionResponseModes},
			Items: []controlOption{
				{Code: "31", Label: "回答方式", Action: actionResponseModes},
				{Code: "32", Label: "视觉风格", Action: actionVisualStyles},
				{Code: "33", Label: voiceLabel, Action: actionVoiceBriefing},
			},
		},
		{
			Code: "4", Title: "WeClaw · 功能",
			Category: controlOption{Code: "4", Label: "WeClaw · 功能", Action: actionFeatureCenter},
			Items: []controlOption{
				{Code: "41", Label: fmt.Sprintf("提示词模板 · %d 项", workflowCount), Action: actionProjectQuickTasks, Query: projectID, Page: 1},
				{Code: "42", Label: saveRecentLabel, Action: actionSaveRecentWorkflow},
				{Code: "43", Label: "素材与交付", Action: actionLibraryCenter},
				{Code: "44", Label: automationLabel, Action: actionAutomations, Page: 1},
			},
		},
		{
			Code: "5", Title: "WeClaw · 设置",
			Category: controlOption{Code: "5", Label: "WeClaw · 设置", Action: actionSettingsCenter},
			Items: []controlOption{
				{Code: "51", Label: "有效配置状态", Action: actionConfigurationStatus},
				{Code: "52", Label: "WeClaw 项目入口", Action: actionProjectCenter},
				{Code: "53", Label: "回复方式与视觉", Action: actionResponseModes},
				{Code: "54", Label: clearLabel, Action: actionConfirmQueueClear},
				{Code: "55", Label: lockLabel, Action: actionConfirmRemoteLock},
			},
		},
		{
			Code: "6", Title: "WeClaw · 诊断",
			Category: controlOption{Code: "6", Label: "WeClaw · 诊断", Action: actionDiagnosticsCenter},
			Items: []controlOption{
				{Code: "61", Label: "为什么没回复", Action: actionNoReplyDiagnostic},
				{Code: "62", Label: "运行状态", Action: actionRuntimeInfo},
				{Code: "63", Label: "使用说明", Action: actionGuide},
				{Code: "64", Label: "刷新首页", Action: actionMain},
			},
		},
	}

	options := make([]controlOption, 0, 48)
	lines := []string{
		"WeClaw 操作总览", "",
		"能力边界：Codex 工作能力与 WeClaw 管理能力分层",
		"版本：" + h.bridgeVersion,
		"WeClaw 项目入口：" + projectName,
		"Codex 线程：" + currentSession,
		"WeClaw 执行：" + taskState,
		"WeClaw 回复：" + preference.Definition().Name,
		"WeClaw 视觉：" + style.Definition().Name,
	}
	for _, section := range sections {
		options = append(options, section.Category)
		options = append(options, section.Items...)
		lines = append(lines, "", fmt.Sprintf("[%s]  %s", section.Code, section.Title), renderControlOptions(section.Items))
	}
	if !h.storeChoiceWithTTL(userID, viewSystemMain, options, controlOption{Action: actionExit}, controlDirectoryTTL) {
		return controlStateFailureResult().Text
	}
	return strings.Join(lines, "\n") + "\n\n回复编号直接操作，0 退出；首页 30 分钟内有效。"
}
