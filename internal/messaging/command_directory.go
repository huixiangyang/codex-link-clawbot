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

	recentLabel := "最近结果"
	if _, exists := h.latestSuccessfulTask(userID, false); !exists {
		recentLabel += " · 暂无"
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
	cancelLabel := "取消当前任务"
	if !h.hasActiveTask(userID) {
		cancelLabel += " · 当前无运行任务"
	}
	clearLabel := "清空等待任务"
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
			Code: "1", Title: "会话管理",
			Category: controlOption{Code: "1", Label: "会话管理", Action: actionSessionMenu},
			Items: []controlOption{
				{Code: "11", Label: "新建会话", Action: actionPromptNewSession},
				{Code: "12", Label: "重命名当前会话", Action: actionPromptRenameSession},
				{Code: "13", Label: "切换会话", Action: actionPickSession},
				{Code: "14", Label: "搜索会话", Action: actionPromptSessionSearch},
				{Code: "15", Label: "查看当前会话", Action: actionCurrentSession},
				{Code: "16", Label: "归档当前会话", Action: actionConfirmArchive},
				{Code: "17", Label: "恢复归档会话", Action: actionPickArchivedSession},
			},
		},
		{
			Code: "2", Title: "项目与工作流",
			Category: controlOption{Code: "2", Label: "项目与工作流", Action: actionProjectCenter},
			Items: []controlOption{
				{Code: "21", Label: "切换项目", Action: actionProjectCenter},
				{Code: "22", Label: fmt.Sprintf("运行快捷任务 · %d 项", workflowCount), Action: actionProjectQuickTasks, Query: projectID, Page: 1, AutoUse: true},
				{Code: "23", Label: "新建快捷任务", Action: actionPromptWorkflowCreate, Query: projectID, Page: 1},
				{Code: "24", Label: "管理快捷任务", Action: actionProjectQuickTasks, Query: projectID, Page: 1},
				{Code: "25", Label: saveRecentLabel, Action: actionSaveRecentWorkflow},
			},
		},
		{
			Code: "3", Title: "任务管理",
			Category: controlOption{Code: "3", Label: "任务管理", Action: actionActivityPage, Page: 1},
			Items: []controlOption{
				{Code: "31", Label: "查看当前任务", Action: actionTaskStatus},
				{Code: "32", Label: recentLabel, Action: actionRecentResult},
				queueToggle,
				{Code: "34", Label: cancelLabel, Action: actionConfirmCancelTask},
				{Code: "35", Label: clearLabel, Action: actionConfirmQueueClear},
			},
		},
		{
			Code: "4", Title: "回答与视觉",
			Category: controlOption{Code: "4", Label: "回答与视觉", Action: actionResponseModes},
			Items: append([]controlOption{
				modeOption("41", "自适应回答", "adaptive"),
				modeOption("42", "阅读卡回答", "reading"),
				modeOption("43", "语音回答", "voice"),
			}, styleOptions...),
		},
		{
			Code: "5", Title: "工具与内容",
			Category: controlOption{Code: "5", Label: "工具与内容", Action: actionMore},
			Items: []controlOption{
				{Code: "51", Label: "素材与交付", Action: actionLibraryCenter},
				{Code: "52", Label: "链接素材", Action: actionLibraryPage, Query: string(LibraryLink), Page: 1},
				{Code: "53", Label: "交付记录", Action: actionLibraryPage, Query: string(LibraryDelivery), Page: 1},
				{Code: "54", Label: automationLabel, Action: actionAutomations, Page: 1},
				{Code: "55", Label: voiceLabel, Action: actionVoiceBriefing},
			},
		},
		{
			Code: "6", Title: "运行与安全",
			Category: controlOption{Code: "6", Label: "运行与安全", Action: actionRuntimeInfo},
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
		"版本：" + h.bridgeVersion,
		"项目：" + projectName,
		"会话：" + currentSession,
		"任务：" + taskState,
		"回答：" + preference.Definition().Name,
		fmt.Sprintf("队列：%d 项等待", queued),
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
