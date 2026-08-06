package messaging

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/huixiangyang/weclaw/internal/session"
	"github.com/huixiangyang/weclaw/internal/visual"
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
	styleOptions := make([]controlOption, 0, len(visual.Styles()))
	for index, definition := range visual.Styles() {
		label := definition.Name + "风格"
		if definition.ID == style {
			label += " · 当前"
		}
		styleOptions = append(styleOptions, controlOption{
			Code: fmt.Sprintf("%d", 44+index), Label: label,
			Action: actionSetVisualStyle, Value: string(definition.ID),
		})
	}
	modeOption := func(code, label string, modeValue string) controlOption {
		if string(preference) == modeValue {
			label += " · 当前"
		}
		return controlOption{Code: code, Label: label, Action: actionSetResponseMode, Value: modeValue}
	}

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
			Code: "1", Title: "Codex · 线程",
			Category: controlOption{Code: "1", Label: "Codex · 线程", Action: actionSessionMenu},
			Items: []controlOption{
				{Code: "11", Label: "新建线程", Action: actionPromptNewSession},
				{Code: "12", Label: "当前线程", Action: actionCurrentSession},
				{Code: "13", Label: "切换线程", Action: actionPickSession},
				{Code: "14", Label: "搜索线程", Action: actionPromptSessionSearch},
				{Code: "15", Label: "分叉当前线程", Action: actionForkThread},
				{Code: "16", Label: "压缩上下文", Action: actionCompactThread},
				{Code: "17", Label: "设置线程目标", Action: actionPromptThreadGoal},
				{Code: "18", Label: "归档当前线程", Action: actionConfirmArchive},
				{Code: "19", Label: "恢复归档线程", Action: actionPickArchivedSession},
			},
		},
		{
			Code: "2", Title: "Codex · 执行能力",
			Category: controlOption{Code: "2", Label: "Codex · 执行能力", Action: actionProjectCenter},
			Items: []controlOption{
				{Code: "21", Label: "WeClaw 项目入口", Action: actionProjectCenter},
				{Code: "22", Label: "线程模型", Action: actionThreadModels},
				{Code: "23", Label: "推理强度", Action: actionThreadEfforts},
				{Code: "24", Label: "审查未提交改动", Action: actionReviewThread},
				{Code: "25", Label: "刷新 Codex 能力", Action: actionProjectCenter},
			},
		},
		{
			Code: "3", Title: "WeClaw · 请求队列",
			Category: controlOption{Code: "3", Label: "WeClaw · 请求队列", Action: actionActivityPage, Page: 1},
			Items: []controlOption{
				{Code: "31", Label: "查看执行状态", Action: actionTaskStatus},
				{Code: "32", Label: "最近执行结果", Action: actionRecentResult},
				queueToggle,
				{Code: "34", Label: cancelLabel, Action: actionConfirmCancelTask},
				{Code: "35", Label: clearLabel, Action: actionConfirmQueueClear},
			},
		},
		{
			Code: "4", Title: "WeClaw · 回复呈现",
			Category: controlOption{Code: "4", Label: "WeClaw · 回复呈现", Action: actionResponseModes},
			Items: append([]controlOption{
				modeOption("41", "自适应回答", "adaptive"),
				modeOption("42", "阅读卡回答", "reading"),
				modeOption("43", "语音回答", "voice"),
			}, styleOptions...),
		},
		{
			Code: "5", Title: "WeClaw · 内容与自动化",
			Category: controlOption{Code: "5", Label: "WeClaw · 内容与自动化", Action: actionMore},
			Items: []controlOption{
				{Code: "51", Label: "素材与交付", Action: actionLibraryCenter},
				{Code: "52", Label: "链接素材", Action: actionLibraryPage, Query: string(LibraryLink), Page: 1},
				{Code: "53", Label: "交付记录", Action: actionLibraryPage, Query: string(LibraryDelivery), Page: 1},
				{Code: "54", Label: automationLabel, Action: actionAutomations, Page: 1},
				{Code: "55", Label: voiceLabel, Action: actionVoiceBriefing},
				{Code: "56", Label: fmt.Sprintf("提示词模板 · WeClaw · %d 项", workflowCount), Action: actionProjectQuickTasks, Query: projectID, Page: 1},
				{Code: "57", Label: saveRecentLabel, Action: actionSaveRecentWorkflow},
			},
		},
		{
			Code: "6", Title: "WeClaw · 运行与安全",
			Category: controlOption{Code: "6", Label: "WeClaw · 运行与安全", Action: actionRuntimeInfo},
			Items: []controlOption{
				{Code: "61", Label: "为什么没回复", Action: actionNoReplyDiagnostic},
				{Code: "62", Label: lockLabel, Action: actionConfirmRemoteLock},
				{Code: "63", Label: "使用说明", Action: actionGuide},
				{Code: "64", Label: "刷新操作总览", Action: actionMain},
			},
		},
	}

	options := make([]controlOption, 0, 48)
	lines := []string{
		"WeClaw 操作总览", "",
		"能力边界：Codex 原生与 WeClaw 增强已分区",
		"版本：" + h.bridgeVersion,
		"WeClaw 项目入口：" + projectName,
		"Codex 线程：" + currentSession,
		"WeClaw 执行：" + taskState,
		"WeClaw 回复：" + preference.Definition().Name,
		fmt.Sprintf("WeClaw 队列：%d 项等待", queued),
	}
	for _, section := range sections {
		options = append(options, section.Category)
		options = append(options, section.Items...)
		lines = append(lines, "", fmt.Sprintf("[%s]  %s", section.Code, section.Title), renderControlOptions(section.Items))
	}
	if !h.storeChoiceWithTTL(userID, viewSystemMain, options, controlOption{Action: actionExit}, controlDirectoryTTL) {
		return controlStateFailureResult().Text
	}
	return strings.Join(lines, "\n") + "\n\n回复编号直接操作，0 退出；总览 30 分钟内有效。"
}
