package messaging

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/huixiangyang/weclaw/codex"
	"github.com/huixiangyang/weclaw/ilink"
	"github.com/huixiangyang/weclaw/session"
	"github.com/huixiangyang/weclaw/visual"
)

const (
	controlStateTTL        = 10 * time.Minute
	controlSessionPageSize = 6
)

type controlMode uint8

const (
	controlChoice controlMode = iota + 1
	controlNewSessionName
	controlRenameSession
	controlWorkingDirectory
	controlSessionSearch
)

type controlAction string

const (
	actionExit                controlAction = "exit"
	actionMain                controlAction = "main"
	actionSessionMenu         controlAction = "session_menu"
	actionCurrentSession      controlAction = "current_session"
	actionPickSession         controlAction = "pick_session"
	actionBrowseSessions      controlAction = "browse_sessions"
	actionPromptSessionSearch controlAction = "prompt_session_search"
	actionSessionPage         controlAction = "session_page"
	actionSessionDetail       controlAction = "session_detail"
	actionUseSession          controlAction = "use_session"
	actionPromptNewSession    controlAction = "prompt_new_session"
	actionPromptRenameSession controlAction = "prompt_rename_session"
	actionConfirmArchive      controlAction = "confirm_archive"
	actionArchiveCurrent      controlAction = "archive_current"
	actionConfirmArchiveItem  controlAction = "confirm_archive_item"
	actionArchiveItem         controlAction = "archive_item"
	actionPickArchivedSession controlAction = "pick_archived_session"
	actionRestoreSession      controlAction = "restore_session"
	actionTaskStatus          controlAction = "task_status"
	actionConfirmCancelTask   controlAction = "confirm_cancel_task"
	actionCancelTask          controlAction = "cancel_task"
	actionActivityPage        controlAction = "activity_page"
	actionActivityDetail      controlAction = "activity_detail"
	actionRuntimeInfo         controlAction = "runtime_info"
	actionPromptWorkingDir    controlAction = "prompt_working_dir"
	actionScheduledReports    controlAction = "scheduled_reports"
	actionScheduledReport     controlAction = "scheduled_report"
	actionVisualStyles        controlAction = "visual_styles"
	actionSetVisualStyle      controlAction = "set_visual_style"
	actionGuide               controlAction = "guide"
)

type controlOption struct {
	Label    string
	Action   controlAction
	Value    string
	Page     int
	Archived bool
	Query    string
	AutoUse  bool
}

// controlState 只保存短期微信交互上下文，不承担任务或会话持久化。
type controlState struct {
	Mode      controlMode
	Prompt    string
	Options   []controlOption
	Back      controlOption
	ExpiresAt time.Time
}

type sessionMatch struct {
	Item  session.ManagedThread
	Score int
}

// handleControlInput 解析菜单、数字回复和高置信度中文控制意图。
// 返回 handled=false 时，原始消息必须原样交给 Codex，不能静默吞掉。
func (h *Handler) handleControlInput(ctx context.Context, userID, text string, hasAttachments bool) (string, bool) {
	text = strings.TrimSpace(text)
	if hasAttachments {
		h.controlStates.Delete(userID)
		return "", false
	}

	if isOneOf(text, "取消", "取消任务", "停止", "停止任务", "停下", "停一下") {
		if _, running := h.activeTasks.Load(userID); running {
			h.controlStates.Delete(userID)
			return h.cancelActiveTask(userID), true
		}
		if _, exists := h.controlStates.LoadAndDelete(userID); exists {
			return "已退出当前操作。直接发送内容即可交给 Codex。", true
		}
		return "当前没有正在执行的任务。发送 / 可以打开操作菜单。", true
	}

	if text == "/" || isOneOf(text, "菜单", "打开菜单", "功能", "操作", "功能菜单", "打开功能") {
		return h.openMainMenu(ctx, userID), true
	}

	if state, ok := h.loadControlState(userID); ok {
		if reply, handled := h.handlePendingControl(ctx, userID, text, state); handled {
			return reply, true
		}
	}

	// 旧斜杠命令不再执行，也不转发到 Codex，避免看似成功却走错通道。
	if strings.HasPrefix(text, "/") {
		return "斜杠命令已取消。发送一个 / 打开操作菜单，或直接用中文描述操作。", true
	}

	if isOneOf(text, "状态", "查看状态", "看下状态", "任务状态", "进度", "任务进度", "查看进度", "进度怎么样", "现在怎么样了", "怎么样了") {
		return h.openTaskStatus(userID), true
	}
	if isOneOf(text, "任务记录", "最近任务", "任务历史", "历史任务") {
		return h.openActivities(userID, 1), true
	}
	if isOneOf(text, "运行中心", "运行信息", "系统信息", "服务信息", "Codex 信息", "Codex信息") {
		return h.openRuntimeInfo(userID), true
	}
	if isOneOf(text, "帮助", "怎么用", "使用说明") {
		return h.openGuide(userID), true
	}
	if isOneOf(text, "视觉风格", "卡片风格", "更换风格", "切换风格", "主题风格") {
		return h.openVisualStyles(userID), true
	}
	if argument, matched := intentArgument(text, []string{"视觉风格", "卡片风格", "更换风格", "切换风格"}); matched {
		argument = cleanIntentArgument(argument)
		if argument == "" {
			return h.openVisualStyles(userID), true
		}
		style, ok := visual.ResolveStyle(argument)
		if !ok {
			return "没有这个视觉风格。发送“视觉风格”查看可选模板。", true
		}
		return h.setVisualStyle(userID, style), true
	}
	if isOneOf(text, "定时巡检", "巡检状态", "定时报告", "报告计划") {
		return h.openScheduledReports(userID, 1), true
	}
	if isOneOf(text, "会话", "查看会话", "看看会话", "会话菜单") {
		return h.openSessionMenu(ctx, userID), true
	}
	if isOneOf(text, "会话列表", "列出会话", "切换会话", "选择会话") {
		return h.openSessionBrowser(ctx, userID, false, ""), true
	}
	if isOneOf(text, "搜索会话", "查找会话", "找会话") {
		return h.promptSessionSearch(userID), true
	}
	if argument, matched := intentArgument(text, []string{"搜索会话", "查找会话", "找会话"}); matched && strings.TrimSpace(argument) != "" {
		return h.openSessionBrowser(ctx, userID, false, cleanIntentArgument(argument)), true
	}
	if isOneOf(text, "当前会话", "这个会话", "会话详情") {
		return h.currentSessionDetail(ctx, userID), true
	}
	if isOneOf(text, "已归档会话", "恢复会话", "找回会话") {
		return h.openSessionPicker(ctx, userID, true, ""), true
	}
	if isOneOf(text, "工作目录", "当前目录", "当前工作目录") {
		return h.openWorkingDirectoryInput(userID), true
	}

	if argument, matched := intentArgument(text, []string{"新建会话", "创建会话", "开一个新会话", "开个新会话"}); matched {
		if h.hasActiveTask(userID) {
			return mutationBusyText(), true
		}
		argument = cleanIntentArgument(argument)
		if argument == "" {
			return h.promptNewSessionName(userID), true
		}
		return h.createSession(ctx, userID, argument), true
	}
	if argument, matched := intentArgument(text, []string{"切换到会话", "切换会话", "切到会话", "进入会话"}); matched {
		if h.hasActiveTask(userID) {
			return mutationBusyText(), true
		}
		return h.openSessionPicker(ctx, userID, false, cleanIntentArgument(argument)), true
	}
	if argument, matched := intentArgument(text, []string{"重命名当前会话", "当前会话改名", "把当前会话改名"}); matched {
		if h.hasActiveTask(userID) {
			return mutationBusyText(), true
		}
		argument = cleanIntentArgument(argument)
		if argument == "" {
			return h.promptRenameSession(userID), true
		}
		return h.renameSession(ctx, userID, argument), true
	}
	if isOneOf(text, "归档当前会话", "把当前会话归档", "归档这个会话") {
		if h.hasActiveTask(userID) {
			return mutationBusyText(), true
		}
		return h.confirmArchiveCurrent(ctx, userID), true
	}
	if argument, matched := intentArgument(text, []string{"恢复会话", "找回会话"}); matched && strings.TrimSpace(argument) != "" {
		if h.hasActiveTask(userID) {
			return mutationBusyText(), true
		}
		return h.openSessionPicker(ctx, userID, true, cleanIntentArgument(argument)), true
	}
	if argument, matched := intentArgument(text, []string{"切换工作目录", "工作目录改为", "把工作目录改为"}); matched {
		if h.hasActiveTask(userID) {
			return mutationBusyText(), true
		}
		argument = cleanIntentArgument(argument)
		if argument == "" {
			return h.openWorkingDirectoryInput(userID), true
		}
		return h.changeWorkingDirectory(argument), true
	}

	return "", false
}

func (h *Handler) handlePendingControl(ctx context.Context, userID, text string, state *controlState) (string, bool) {
	if isOneOf(text, "返回", "回到菜单") {
		if !h.controlStates.CompareAndDelete(userID, state) {
			return "操作状态已经变化。发送 / 重新打开菜单。", true
		}
		return h.executeControlAction(ctx, userID, state.Back), true
	}

	switch state.Mode {
	case controlChoice:
		choice, err := strconv.Atoi(text)
		if err != nil {
			if option, ok := controlNavigationOption(text, state.Options); ok {
				if !h.controlStates.CompareAndDelete(userID, state) {
					return "操作状态已经变化。发送 / 重新打开菜单。", true
				}
				return h.executeControlAction(ctx, userID, option), true
			}
			// 非数字内容退出选择态，继续尝试自然语言；普通内容最终进入 Codex。
			h.controlStates.CompareAndDelete(userID, state)
			return "", false
		}
		if choice == 0 {
			if !h.controlStates.CompareAndDelete(userID, state) {
				return "操作状态已经变化。发送 / 重新打开菜单。", true
			}
			return h.executeControlAction(ctx, userID, state.Back), true
		}
		if choice < 1 || choice > len(state.Options) {
			return state.Prompt + fmt.Sprintf("\n\n请输入 1-%d，或回复 0 返回。", len(state.Options)), true
		}
		if !h.controlStates.CompareAndDelete(userID, state) {
			return "这个选项已经处理。发送 / 重新打开菜单。", true
		}
		return h.executeControlAction(ctx, userID, state.Options[choice-1]), true
	case controlNewSessionName:
		if !h.controlStates.CompareAndDelete(userID, state) {
			return "这个操作已经处理。发送 / 重新打开菜单。", true
		}
		if h.hasActiveTask(userID) {
			return mutationBusyText(), true
		}
		if text == "0" || isOneOf(text, "跳过", "不命名") {
			return h.createSession(ctx, userID, ""), true
		}
		return h.createSession(ctx, userID, text), true
	case controlRenameSession:
		if text == "0" {
			if !h.controlStates.CompareAndDelete(userID, state) {
				return "这个操作已经处理。发送 / 重新打开菜单。", true
			}
			return h.openSessionMenu(ctx, userID), true
		}
		if !h.controlStates.CompareAndDelete(userID, state) {
			return "这个操作已经处理。发送 / 重新打开菜单。", true
		}
		if h.hasActiveTask(userID) {
			return mutationBusyText(), true
		}
		return h.renameSession(ctx, userID, text), true
	case controlWorkingDirectory:
		if text == "0" {
			if !h.controlStates.CompareAndDelete(userID, state) {
				return "这个操作已经处理。发送 / 重新打开菜单。", true
			}
			return h.openMainMenu(ctx, userID), true
		}
		if !h.controlStates.CompareAndDelete(userID, state) {
			return "这个操作已经处理。发送 / 重新打开菜单。", true
		}
		if h.hasActiveTask(userID) {
			return mutationBusyText(), true
		}
		return h.changeWorkingDirectory(text), true
	case controlSessionSearch:
		if text == "0" {
			if !h.controlStates.CompareAndDelete(userID, state) {
				return "这个操作已经处理。发送 / 重新打开菜单。", true
			}
			return h.openSessionMenu(ctx, userID), true
		}
		if !h.controlStates.CompareAndDelete(userID, state) {
			return "这个操作已经处理。发送 / 重新打开菜单。", true
		}
		return h.openSessionBrowser(ctx, userID, false, text), true
	default:
		h.controlStates.Delete(userID)
		return "", false
	}
}

func (h *Handler) executeControlAction(ctx context.Context, userID string, option controlOption) string {
	switch option.Action {
	case actionExit:
		return "已退出菜单。直接发送文字、图片或文件即可交给 Codex。"
	case actionMain, "":
		return h.openMainMenu(ctx, userID)
	case actionSessionMenu:
		return h.openSessionMenu(ctx, userID)
	case actionCurrentSession:
		return h.currentSessionDetail(ctx, userID)
	case actionPickSession:
		return h.openSessionPicker(ctx, userID, false, "")
	case actionBrowseSessions:
		return h.openSessionBrowser(ctx, userID, false, "")
	case actionPromptSessionSearch:
		return h.promptSessionSearch(userID)
	case actionSessionPage:
		return h.openSessionPickerPage(ctx, userID, option.Archived, option.Query, option.Page, option.AutoUse)
	case actionSessionDetail:
		return h.sessionDetail(ctx, userID, option)
	case actionUseSession:
		if h.hasActiveTask(userID) {
			return mutationBusyText()
		}
		return h.useSession(ctx, userID, option.Value)
	case actionPromptNewSession:
		if h.hasActiveTask(userID) {
			return mutationBusyText()
		}
		return h.promptNewSessionName(userID)
	case actionPromptRenameSession:
		if h.hasActiveTask(userID) {
			return mutationBusyText()
		}
		return h.promptRenameSession(userID)
	case actionConfirmArchive:
		if h.hasActiveTask(userID) {
			return mutationBusyText()
		}
		return h.confirmArchiveCurrent(ctx, userID)
	case actionArchiveCurrent:
		if h.hasActiveTask(userID) {
			return mutationBusyText()
		}
		return h.archiveCurrentSession(ctx, userID)
	case actionConfirmArchiveItem:
		if h.hasActiveTask(userID) {
			return mutationBusyText()
		}
		return h.confirmArchiveSession(ctx, userID, option)
	case actionArchiveItem:
		if h.hasActiveTask(userID) {
			return mutationBusyText()
		}
		return h.archiveSession(ctx, userID, option.Value)
	case actionPickArchivedSession:
		return h.openSessionPicker(ctx, userID, true, "")
	case actionRestoreSession:
		if h.hasActiveTask(userID) {
			return mutationBusyText()
		}
		return h.restoreSession(ctx, userID, option.Value)
	case actionTaskStatus:
		return h.openTaskStatus(userID)
	case actionConfirmCancelTask:
		return h.confirmCancelTask(userID)
	case actionCancelTask:
		return h.cancelActiveTask(userID)
	case actionActivityPage:
		return h.openActivities(userID, option.Page)
	case actionActivityDetail:
		return h.openActivityDetail(userID, option.Value, option.Page)
	case actionRuntimeInfo:
		return h.openRuntimeInfo(userID)
	case actionPromptWorkingDir:
		return h.openWorkingDirectoryInput(userID)
	case actionScheduledReports:
		return h.openScheduledReports(userID, option.Page)
	case actionScheduledReport:
		return h.openScheduledReport(userID, option.Value, option.Page)
	case actionVisualStyles:
		return h.openVisualStyles(userID)
	case actionSetVisualStyle:
		return h.setVisualStyle(userID, visual.Style(option.Value))
	case actionGuide:
		return h.openGuide(userID)
	default:
		return "这个操作已经失效。发送 / 重新打开菜单。"
	}
}

func (h *Handler) openMainMenu(ctx context.Context, userID string) string {
	currentName := "未创建"
	if threadAgent, err := h.sessionContext(); err == nil {
		if current, currentErr := h.sessions.Current(ctx, userID, threadAgent); currentErr == nil {
			currentName = threadTitle(current.Info)
		} else if !errors.Is(currentErr, session.ErrNoActive) {
			currentName = "暂不可读"
		}
	}
	taskState := "空闲"
	if _, running := h.activeTasks.Load(userID); running {
		taskState = "运行中"
	}
	statuses := h.scheduledReportStatuses(userID)
	options := []controlOption{
		{Label: "会话", Action: actionSessionMenu},
		{Label: "任务状态", Action: actionTaskStatus},
		{Label: "任务记录", Action: actionActivityPage, Page: 1},
		{Label: "运行中心", Action: actionRuntimeInfo},
		{Label: "工作目录", Action: actionPromptWorkingDir},
	}
	if len(statuses) > 0 {
		options = append(options, controlOption{Label: "定时巡检", Action: actionScheduledReports, Page: 1})
	}
	if h.visual != nil && h.visualStyles != nil {
		options = append(options, controlOption{Label: "视觉风格", Action: actionVisualStyles})
	}
	options = append(options, controlOption{Label: "使用说明", Action: actionGuide})
	lines := []string{
		"WeClaw",
		"",
		"版本：" + h.bridgeVersion,
		"会话：" + currentName,
		"状态：" + taskState,
	}
	if h.activities != nil {
		lines = append(lines, fmt.Sprintf("记录：%d 条", len(h.activities.List(userID))))
	}
	if len(statuses) > 0 {
		lines = append(lines, fmt.Sprintf("巡检：%d 项", len(statuses)))
	}
	lines = append(lines, "", renderControlOptions(options))
	prompt := strings.Join(lines, "\n")
	h.storeChoice(userID, prompt, options, actionExit)
	return prompt + "\n\n回复数字即可，0 退出。"
}

func (h *Handler) openTaskStatus(userID string) string {
	options := []controlOption{{Label: "刷新状态", Action: actionTaskStatus}}
	if h.hasActiveTask(userID) {
		options = append(options, controlOption{Label: "取消当前任务", Action: actionConfirmCancelTask})
	} else {
		options = append(options, controlOption{Label: "运行中心", Action: actionRuntimeInfo})
	}
	options = append(options, controlOption{Label: "任务记录", Action: actionActivityPage, Page: 1})
	prompt := h.buildTaskStatus(userID) + "\n\n" + renderControlOptions(options)
	h.storeChoice(userID, prompt, options, actionMain)
	return prompt + "\n\n回复数字操作，0 返回。"
}

func (h *Handler) confirmCancelTask(userID string) string {
	if !h.hasActiveTask(userID) {
		return h.openTaskStatus(userID)
	}
	options := []controlOption{{Label: "确认取消任务", Action: actionCancelTask}}
	prompt := "准备取消当前任务\n\n取消后，本次迟到的进度和最终结果都不会发送。\n\n" + renderControlOptions(options)
	h.storeChoice(userID, prompt, options, actionTaskStatus)
	return prompt + "\n\n回复 1 确认，0 返回任务状态。"
}

func (h *Handler) openRuntimeInfo(userID string) string {
	options := []controlOption{
		{Label: "工作目录", Action: actionPromptWorkingDir},
		{Label: "刷新运行中心", Action: actionRuntimeInfo},
	}
	prompt := h.buildStatus() + "\n\n" + renderControlOptions(options)
	h.storeChoice(userID, prompt, options, actionMain)
	return prompt + "\n\n回复数字操作，0 返回。"
}

func (h *Handler) openGuide(userID string) string {
	options := []controlOption{
		{Label: "会话中心", Action: actionSessionMenu},
		{Label: "任务状态", Action: actionTaskStatus},
	}
	if h.visual != nil && h.visualStyles != nil {
		options = append(options, controlOption{Label: "视觉风格", Action: actionVisualStyles})
	}
	prompt := "使用说明\n\n" + controlGuide() + "\n\n" + renderControlOptions(options)
	h.storeChoice(userID, prompt, options, actionMain)
	return prompt + "\n\n回复数字继续，0 返回。"
}

func (h *Handler) openVisualStyles(userID string) string {
	if h.visual == nil || h.visualStyles == nil {
		return "视觉卡片当前不可用。"
	}
	current := h.visualStyles.Get(userID)
	definitions := visual.Styles()
	options := make([]controlOption, 0, len(definitions))
	for _, definition := range definitions {
		label := definition.Name + " · " + definition.Description
		options = append(options, controlOption{Label: label, Action: actionSetVisualStyle, Value: string(definition.ID)})
	}
	currentDefinition := current.Definition()
	prompt := strings.Join([]string{
		"视觉风格",
		"",
		"当前：" + currentDefinition.Name,
		"说明：选择后立即应用，同时用于控制卡和长回复。",
		"",
		renderControlOptions(options),
	}, "\n")
	h.storeChoice(userID, prompt, options, actionMain)
	return prompt + "\n\n回复数字切换并查看效果，0 返回。"
}

func (h *Handler) setVisualStyle(userID string, style visual.Style) string {
	if h.visual == nil || h.visualStyles == nil {
		return "视觉卡片当前不可用。"
	}
	if !style.Valid() {
		return "没有这个视觉风格。发送“视觉风格”查看可选模板。"
	}
	if err := h.visualStyles.Set(userID, style); err != nil {
		log.Printf("[visual] failed to persist style for %s: %v", ilink.LogLabel(userID), err)
		return "视觉风格保存失败，请稍后重试。"
	}
	definition := style.Definition()
	options := []controlOption{
		{Label: "返回主菜单", Action: actionMain},
		{Label: "选择其他风格", Action: actionVisualStyles},
	}
	prompt := strings.Join([]string{
		"视觉风格已切换",
		"",
		"当前：" + definition.Name,
		"质感：" + definition.Description,
		"范围：控制卡与长回复",
		"",
		renderControlOptions(options),
	}, "\n")
	h.storeChoice(userID, prompt, options, actionMain)
	return prompt + "\n\n当前卡片就是新风格预览；回复数字继续，0 返回。"
}

func (h *Handler) openSessionMenu(ctx context.Context, userID string) string {
	currentName := "未创建"
	stats := session.Stats{}
	if h.sessions != nil {
		stats = h.sessions.Stats(userID)
	}
	if threadAgent, err := h.sessionContext(); err == nil {
		if current, currentErr := h.sessions.Current(ctx, userID, threadAgent); currentErr == nil {
			currentName = threadTitle(current.Info)
		}
	}
	options := []controlOption{
		{Label: "当前会话", Action: actionCurrentSession},
		{Label: "会话列表", Action: actionBrowseSessions},
		{Label: "搜索会话", Action: actionPromptSessionSearch},
		{Label: "新建会话", Action: actionPromptNewSession},
		{Label: "重命名当前会话", Action: actionPromptRenameSession},
		{Label: "归档当前会话", Action: actionConfirmArchive},
		{Label: "恢复已归档会话", Action: actionPickArchivedSession},
	}
	prompt := strings.Join([]string{
		"会话中心",
		"",
		"当前：" + currentName,
		fmt.Sprintf("可用：%d", stats.Active),
		fmt.Sprintf("已归档：%d", stats.Archived),
		"",
		renderControlOptions(options),
	}, "\n")
	h.storeChoice(userID, prompt, options, actionMain)
	return prompt + "\n\n回复数字即可，0 返回。"
}

func (h *Handler) currentSessionDetail(ctx context.Context, userID string) string {
	threadAgent, err := h.sessionContext()
	if err != nil {
		return err.Error()
	}
	current, err := h.sessions.Current(ctx, userID, threadAgent)
	if err != nil {
		if errors.Is(err, session.ErrNoActive) {
			options := []controlOption{
				{Label: "新建会话", Action: actionPromptNewSession},
				{Label: "恢复已归档会话", Action: actionPickArchivedSession},
			}
			prompt := "当前没有会话。发送普通内容会自动创建，也可以现在管理。\n\n" + renderControlOptions(options)
			h.storeChoice(userID, prompt, options, actionSessionMenu)
			return prompt + "\n\n回复数字即可，0 返回。"
		}
		return formatSessionError(err)
	}
	options := []controlOption{
		{Label: "重命名当前会话", Action: actionPromptRenameSession},
		{Label: "切换其他会话", Action: actionPickSession},
		{Label: "归档当前会话", Action: actionConfirmArchive},
	}
	prompt := formatSessionDetail("当前会话", current) + "\n\n" + renderControlOptions(options)
	h.storeChoice(userID, prompt, options, actionSessionMenu)
	return prompt + "\n\n回复数字管理，0 返回。"
}

func (h *Handler) openSessionPicker(ctx context.Context, userID string, archived bool, query string) string {
	return h.openSessionPickerPage(ctx, userID, archived, query, 1, true)
}

func (h *Handler) openSessionBrowser(ctx context.Context, userID string, archived bool, query string) string {
	return h.openSessionPickerPage(ctx, userID, archived, strings.TrimSpace(query), 1, false)
}

func (h *Handler) openSessionPickerPage(ctx context.Context, userID string, archived bool, query string, pageNumber int, autoUse bool) string {
	threadAgent, err := h.sessionContext()
	if err != nil {
		return err.Error()
	}
	var page session.Page
	if query != "" {
		all, listErr := h.sessions.List(ctx, userID, threadAgent, archived, 1, 1000)
		if listErr != nil {
			return formatSessionError(listErr)
		}
		matches := matchSessions(all.Items, query)
		exactWinner := len(matches) > 0 && matches[0].Score == 0 && (len(matches) == 1 || matches[1].Score > 0)
		if autoUse && (len(matches) == 1 || exactWinner) {
			if archived {
				return h.restoreSession(ctx, userID, matches[0].Item.Info.ID)
			}
			return h.useSession(ctx, userID, matches[0].Item.Info.ID)
		}
		items := make([]session.ManagedThread, 0, len(matches))
		for _, match := range matches {
			items = append(items, match.Item)
		}
		page, err = paginateManagedThreads(items, pageNumber, controlSessionPageSize)
	} else {
		page, err = h.sessions.List(ctx, userID, threadAgent, archived, pageNumber, controlSessionPageSize)
	}
	if err != nil {
		return formatSessionError(err)
	}
	if len(page.Items) == 0 {
		if query != "" {
			return fmt.Sprintf("没有找到包含“%s”的%s。发送“会话列表”查看全部候选。", query, sessionKind(archived))
		}
		if archived {
			return "没有已归档会话。"
		}
		return "还没有可切换的会话。发送“新建会话”即可创建。"
	}

	action := actionSessionDetail
	if autoUse {
		action = actionUseSession
		if archived {
			action = actionRestoreSession
		}
	}
	options := make([]controlOption, 0, controlSessionPageSize+2)
	for _, item := range page.Items {
		label := threadTitle(item.Info) + " · " + formatThreadStatus(item.Info.Status)
		if item.Current {
			label += " · 当前"
		}
		if item.Info.IsPinned {
			label += " · 置顶"
		}
		if item.Unavailable {
			label += " · 无法读取"
		}
		options = append(options, controlOption{
			Label: label, Action: action, Value: item.Info.ID,
			Page: page.Number, Archived: archived, Query: query, AutoUse: autoUse,
		})
	}
	if page.Number > 1 {
		options = append(options, controlOption{
			Label: fmt.Sprintf("上一页 · %d/%d", page.Number-1, page.TotalPages), Action: actionSessionPage,
			Page: page.Number - 1, Archived: archived, Query: query, AutoUse: autoUse,
		})
	}
	if page.Number < page.TotalPages {
		options = append(options, controlOption{
			Label: fmt.Sprintf("下一页 · %d/%d", page.Number+1, page.TotalPages), Action: actionSessionPage,
			Page: page.Number + 1, Archived: archived, Query: query, AutoUse: autoUse,
		})
	}
	title := "选择会话"
	if !autoUse {
		title = "会话列表"
	}
	if archived {
		title = "恢复会话"
	}
	if query != "" {
		title += "：" + query
	}
	lines := []string{
		title,
		"",
		fmt.Sprintf("页码：%d / %d", page.Number, page.TotalPages),
		fmt.Sprintf("总数：%d", page.Total),
	}
	if query != "" {
		lines = append(lines, "筛选："+query)
	}
	lines = append(lines, "", renderControlOptions(options))
	prompt := strings.Join(lines, "\n")
	h.storeChoice(userID, prompt, options, actionSessionMenu)
	return prompt + "\n\n回复数字，或直接说“下一页”“上一页”；0 返回。"
}

func (h *Handler) promptSessionSearch(userID string) string {
	prompt := "搜索会话\n\n发送名称、短编号或记得的连续字符，回复 0 返回。"
	h.storeInput(userID, controlSessionSearch, prompt, actionSessionMenu)
	return prompt
}

func (h *Handler) sessionDetail(ctx context.Context, userID string, source controlOption) string {
	threadAgent, err := h.sessionContext()
	if err != nil {
		return err.Error()
	}
	detail, err := h.sessions.Detail(ctx, userID, threadAgent, source.Value, source.Archived)
	if err != nil {
		return formatSessionError(err)
	}
	if source.Page <= 0 {
		source.Page = 1
	}
	source.Action = actionSessionDetail
	source.AutoUse = false
	back := controlOption{
		Action: actionSessionPage, Page: source.Page, Archived: source.Archived,
		Query: source.Query, AutoUse: false,
	}
	options := make([]controlOption, 0, 3)
	if source.Archived {
		options = append(options, controlOption{Label: "恢复这个会话", Action: actionRestoreSession, Value: detail.Info.ID})
	} else if detail.Current {
		options = append(options,
			controlOption{Label: "重命名当前会话", Action: actionPromptRenameSession},
			controlOption{
				Label: "归档这个会话", Action: actionConfirmArchiveItem, Value: detail.Info.ID,
				Page: source.Page, Query: source.Query,
			},
		)
	} else {
		options = append(options,
			controlOption{Label: "切换到这个会话", Action: actionUseSession, Value: detail.Info.ID},
			controlOption{
				Label: "归档这个会话", Action: actionConfirmArchiveItem, Value: detail.Info.ID,
				Page: source.Page, Query: source.Query,
			},
		)
	}
	options = append(options, controlOption{
		Label: "返回会话列表", Action: actionSessionPage, Page: source.Page,
		Archived: source.Archived, Query: source.Query, AutoUse: false,
	})
	title := "会话详情"
	if source.Archived {
		title = "归档会话详情"
	}
	prompt := formatSessionDetail(title, detail) + "\n\n" + renderControlOptions(options)
	h.storeChoiceWithBack(userID, prompt, options, back)
	return prompt + "\n\n回复数字管理，0 返回原列表。"
}

func (h *Handler) confirmArchiveSession(ctx context.Context, userID string, source controlOption) string {
	threadAgent, err := h.sessionContext()
	if err != nil {
		return err.Error()
	}
	detail, err := h.sessions.Detail(ctx, userID, threadAgent, source.Value, false)
	if err != nil {
		return formatSessionError(err)
	}
	if source.Page <= 0 {
		source.Page = 1
	}
	back := controlOption{
		Action: actionSessionDetail, Value: detail.Info.ID, Page: source.Page,
		Query: source.Query, AutoUse: false,
	}
	options := []controlOption{{Label: "确认归档这个会话", Action: actionArchiveItem, Value: detail.Info.ID}}
	prompt := "准备归档会话：" + threadTitle(detail.Info) + "\n\n归档后可从“恢复已归档会话”找回。\n\n" + renderControlOptions(options)
	h.storeChoiceWithBack(userID, prompt, options, back)
	return prompt + "\n\n回复 1 确认，0 返回会话详情。"
}

func (h *Handler) archiveSession(ctx context.Context, userID, threadID string) string {
	threadAgent, err := h.sessionContext()
	if err != nil {
		return err.Error()
	}
	if _, err := h.sessions.Archive(ctx, userID, threadAgent, threadID); err != nil {
		return formatSessionError(err)
	}
	currentName := "未创建"
	if current, currentErr := h.sessions.Current(ctx, userID, threadAgent); currentErr == nil {
		currentName = threadTitle(current.Info)
	} else if !errors.Is(currentErr, session.ErrNoActive) {
		currentName = "暂不可读"
	}
	options := []controlOption{
		{Label: "会话列表", Action: actionBrowseSessions},
		{Label: "恢复已归档会话", Action: actionPickArchivedSession},
	}
	prompt := "会话已归档。\n当前：" + currentName + "\n\n" + renderControlOptions(options)
	h.storeChoice(userID, prompt, options, actionSessionMenu)
	return prompt + "\n\n回复数字继续，0 返回会话中心。"
}

func paginateManagedThreads(items []session.ManagedThread, pageNumber, pageSize int) (session.Page, error) {
	if pageNumber <= 0 {
		pageNumber = 1
	}
	if pageSize <= 0 {
		pageSize = controlSessionPageSize
	}
	totalPages := (len(items) + pageSize - 1) / pageSize
	if totalPages == 0 {
		totalPages = 1
	}
	if pageNumber > totalPages {
		return session.Page{}, fmt.Errorf("page %d exceeds total pages %d", pageNumber, totalPages)
	}
	start := (pageNumber - 1) * pageSize
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return session.Page{
		Items: items[start:end], Number: pageNumber,
		TotalPages: totalPages, Total: len(items),
	}, nil
}

func (h *Handler) promptNewSessionName(userID string) string {
	prompt := "新建会话\n\n发送会话名称；回复 0 或“跳过”可创建未命名会话。"
	h.storeInput(userID, controlNewSessionName, prompt, actionSessionMenu)
	return prompt
}

func (h *Handler) promptRenameSession(userID string) string {
	prompt := "重命名会话\n\n发送新的会话名称，回复 0 返回。"
	h.storeInput(userID, controlRenameSession, prompt, actionSessionMenu)
	return prompt
}

func (h *Handler) createSession(ctx context.Context, userID, name string) string {
	threadAgent, err := h.sessionContext()
	if err != nil {
		return err.Error()
	}
	thread, err := h.sessions.New(ctx, userID, threadAgent, strings.TrimSpace(name))
	if err != nil {
		return formatSessionError(err)
	}
	return h.sessionSuccess(userID, "已创建并切换到新会话。", thread)
}

func (h *Handler) useSession(ctx context.Context, userID, threadID string) string {
	threadAgent, err := h.sessionContext()
	if err != nil {
		return err.Error()
	}
	thread, err := h.sessions.Use(ctx, userID, threadAgent, threadID)
	if err != nil {
		return formatSessionError(err)
	}
	return h.sessionSuccess(userID, "已切换会话。", thread)
}

func (h *Handler) renameSession(ctx context.Context, userID, name string) string {
	threadAgent, err := h.sessionContext()
	if err != nil {
		return err.Error()
	}
	thread, err := h.sessions.Rename(ctx, userID, threadAgent, strings.TrimSpace(name))
	if err != nil {
		return formatSessionError(err)
	}
	return h.sessionSuccess(userID, "会话已重命名。", thread)
}

func (h *Handler) confirmArchiveCurrent(ctx context.Context, userID string) string {
	threadAgent, err := h.sessionContext()
	if err != nil {
		return err.Error()
	}
	current, err := h.sessions.Current(ctx, userID, threadAgent)
	if err != nil {
		return formatSessionError(err)
	}
	options := []controlOption{{Label: "确认归档", Action: actionArchiveCurrent}}
	prompt := "准备归档会话：" + threadTitle(current.Info) + "\n\n" + renderControlOptions(options)
	h.storeChoice(userID, prompt, options, actionSessionMenu)
	return prompt + "\n\n回复 1 确认，0 返回。"
}

func (h *Handler) archiveCurrentSession(ctx context.Context, userID string) string {
	threadAgent, err := h.sessionContext()
	if err != nil {
		return err.Error()
	}
	nextActive, err := h.sessions.Archive(ctx, userID, threadAgent, "")
	if err != nil {
		return formatSessionError(err)
	}
	if nextActive == "" {
		options := []controlOption{
			{Label: "新建会话", Action: actionPromptNewSession},
			{Label: "恢复已归档会话", Action: actionPickArchivedSession},
		}
		prompt := "会话已归档。\n当前：未创建\n\n下一条普通消息会自动创建新会话。\n\n" + renderControlOptions(options)
		h.storeChoice(userID, prompt, options, actionSessionMenu)
		return prompt + "\n\n回复数字继续，0 返回会话中心。"
	}
	currentName := session.ShortCode(nextActive)
	if current, currentErr := h.sessions.Current(ctx, userID, threadAgent); currentErr == nil {
		currentName = threadTitle(current.Info)
	}
	options := []controlOption{
		{Label: "查看当前会话", Action: actionCurrentSession},
		{Label: "恢复已归档会话", Action: actionPickArchivedSession},
		{Label: "会话中心", Action: actionSessionMenu},
	}
	prompt := "会话已归档。\n当前：" + currentName + "\n\n" + renderControlOptions(options)
	h.storeChoice(userID, prompt, options, actionSessionMenu)
	return prompt + "\n\n回复数字继续，0 返回会话中心。"
}

func (h *Handler) restoreSession(ctx context.Context, userID, threadID string) string {
	threadAgent, err := h.sessionContext()
	if err != nil {
		return err.Error()
	}
	thread, err := h.sessions.Restore(ctx, userID, threadAgent, threadID)
	if err != nil {
		return formatSessionError(err)
	}
	options := make([]controlOption, 0, 3)
	stats := h.sessions.Stats(userID)
	if stats.CurrentID == thread.ID {
		options = append(options, controlOption{Label: "查看当前会话", Action: actionCurrentSession})
	} else {
		options = append(options,
			controlOption{Label: "切换到已恢复会话", Action: actionUseSession, Value: thread.ID},
			controlOption{Label: "查看当前会话", Action: actionCurrentSession},
		)
	}
	options = append(options, controlOption{Label: "会话中心", Action: actionSessionMenu})
	prompt := "会话已恢复。\n" + formatThreadIdentity(thread) + "\n\n" + renderControlOptions(options)
	h.storeChoice(userID, prompt, options, actionSessionMenu)
	return prompt + "\n\n回复数字继续，0 返回会话中心。"
}

func (h *Handler) sessionSuccess(userID, headline string, thread codex.ThreadInfo) string {
	options := []controlOption{
		{Label: "查看当前会话", Action: actionCurrentSession},
		{Label: "会话列表", Action: actionBrowseSessions},
		{Label: "会话中心", Action: actionSessionMenu},
	}
	prompt := headline + "\n" + formatThreadIdentity(thread) + "\n\n" + renderControlOptions(options)
	h.storeChoice(userID, prompt, options, actionSessionMenu)
	return prompt + "\n\n回复数字继续，或直接发送内容开始对话；0 返回会话中心。"
}

func (h *Handler) openWorkingDirectoryInput(userID string) string {
	if h.codex == nil {
		return "Codex 当前不可用。"
	}
	current := h.codex.Info().Cwd
	if h.hasActiveTask(userID) {
		return "当前工作目录：" + current + "\n任务运行期间不能修改工作目录。"
	}
	prompt := "工作目录\n\n当前：" + current + "\n\n发送新的绝对路径，回复 0 返回。"
	h.storeInput(userID, controlWorkingDirectory, prompt, actionMain)
	return prompt
}

func (h *Handler) changeWorkingDirectory(value string) string {
	if h.codex == nil {
		return "Codex 当前不可用。"
	}
	value = strings.TrimSpace(value)
	if value == "~" || strings.HasPrefix(value, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Sprintf("无法解析主目录：%v", err)
		}
		if value == "~" {
			value = home
		} else {
			value = filepath.Join(home, value[2:])
		}
	}
	if !filepath.IsAbs(value) {
		return "工作目录必须是绝对路径，或使用 ~/ 开头的路径。"
	}
	value = filepath.Clean(value)
	info, err := os.Stat(value)
	if err != nil {
		return "目录不存在：" + value
	}
	if !info.IsDir() {
		return "不是目录：" + value
	}
	h.codex.SetCwd(value)
	log.Printf("[handler] updated codex cwd: %s", value)
	return "工作目录已切换：" + value
}

func (h *Handler) storeChoice(userID, prompt string, options []controlOption, back controlAction) {
	h.storeChoiceWithBack(userID, prompt, options, controlOption{Action: back})
}

// storeChoiceWithBack 为分页详情保留完整返回位置，避免移动端反复翻页。
func (h *Handler) storeChoiceWithBack(userID, prompt string, options []controlOption, back controlOption) {
	h.controlStates.Store(userID, &controlState{
		Mode: controlChoice, Prompt: prompt, Options: append([]controlOption(nil), options...),
		Back: back, ExpiresAt: time.Now().Add(controlStateTTL),
	})
}

func (h *Handler) storeInput(userID string, mode controlMode, prompt string, back controlAction) {
	h.controlStates.Store(userID, &controlState{
		Mode: mode, Prompt: prompt, Back: controlOption{Action: back}, ExpiresAt: time.Now().Add(controlStateTTL),
	})
}

func (h *Handler) loadControlState(userID string) (*controlState, bool) {
	value, ok := h.controlStates.Load(userID)
	if !ok {
		return nil, false
	}
	state, ok := value.(*controlState)
	if !ok || time.Now().After(state.ExpiresAt) {
		h.controlStates.CompareAndDelete(userID, value)
		return nil, false
	}
	return state, true
}

func (h *Handler) hasActiveTask(userID string) bool {
	_, ok := h.activeTasks.Load(userID)
	return ok
}

func renderControlOptions(options []controlOption) string {
	lines := make([]string, 0, len(options))
	for index, option := range options {
		lines = append(lines, fmt.Sprintf("%d  %s", index+1, option.Label))
	}
	return strings.Join(lines, "\n")
}

func controlNavigationOption(text string, options []controlOption) (controlOption, bool) {
	forward := isOneOf(text, "下一页", "下页", "往后", "后面")
	backward := isOneOf(text, "上一页", "上页", "往前", "前面")
	if !forward && !backward {
		return controlOption{}, false
	}
	for _, option := range options {
		if option.Action != actionSessionPage && option.Action != actionScheduledReports && option.Action != actionActivityPage {
			continue
		}
		if forward && strings.HasPrefix(option.Label, "下一页") {
			return option, true
		}
		if backward && strings.HasPrefix(option.Label, "上一页") {
			return option, true
		}
	}
	return controlOption{}, false
}

func controlGuide() string {
	return strings.Join([]string{
		"直接发送文字、图片或文件，内容会交给 Codex。",
		"较长回复自动整理为阅读卡片，回复“文字版”可获取可复制原文。",
		"发送 / 打开操作菜单，回复数字或“下一页”“上一页”完成选择。",
		"发送“视觉风格”可在刊物、构筑和黑标之间切换，选择会自动保存。",
		"也可以直接说“新建会话”“搜索会话”“切换会话 登录”“运行中心”或“工作目录”。",
		"任务运行时发送“状态”查看进度，发送“取消”停止任务。",
	}, "\n")
}

func mutationBusyText() string {
	return "当前任务仍在运行，暂时不能修改会话或工作目录。发送“状态”查看进度，或发送“取消”停止任务。"
}

func sessionKind(archived bool) string {
	if archived {
		return "已归档会话"
	}
	return "会话"
}

func isOneOf(value string, candidates ...string) bool {
	value = normalizeControlPhrase(value)
	for _, candidate := range candidates {
		if strings.EqualFold(value, normalizeControlPhrase(candidate)) {
			return true
		}
	}
	return false
}

func normalizeControlPhrase(value string) string {
	return strings.TrimSpace(strings.TrimRight(strings.TrimSpace(value), "？?。！!"))
}

func intentArgument(value string, prefixes []string) (string, bool) {
	for _, prefix := range prefixes {
		if strings.HasPrefix(value, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(value, prefix)), true
		}
	}
	return "", false
}

func cleanIntentArgument(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimLeft(value, "：:，,。 ")
	for _, prefix := range []string{"叫做", "名字叫", "名为", "改为", "叫", "为"} {
		if strings.HasPrefix(value, prefix) {
			value = strings.TrimSpace(strings.TrimPrefix(value, prefix))
			break
		}
	}
	return strings.Trim(value, " \t\r\n：:，,。\"“”")
}

func matchSessions(items []session.ManagedThread, query string) []sessionMatch {
	queryKey := fuzzyKey(query)
	if queryKey == "" {
		return nil
	}
	matches := make([]sessionMatch, 0, len(items))
	for _, item := range items {
		score := bestFuzzyScore(queryKey,
			fuzzyKey(threadSearchTitle(item.Info)),
			fuzzyKey(session.ShortCode(item.Info.ID)),
			fuzzyKey(item.Info.ID),
		)
		if score >= 0 {
			matches = append(matches, sessionMatch{Item: item, Score: score})
		}
	}
	// 同分时保留 Manager 已经给出的最近活动顺序。
	sort.SliceStable(matches, func(i, j int) bool { return matches[i].Score < matches[j].Score })
	return matches
}

func fuzzyKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) || strings.ContainsRune("-_：:，,。./\\", r) {
			return -1
		}
		return r
	}, value)
}

func bestFuzzyScore(query string, candidates ...string) int {
	best := -1
	for _, candidate := range candidates {
		score := fuzzyScore(query, candidate)
		if score >= 0 && (best < 0 || score < best) {
			best = score
		}
	}
	return best
}

func fuzzyScore(query, candidate string) int {
	switch {
	case query == "" || candidate == "":
		return -1
	case query == candidate:
		return 0
	case strings.HasPrefix(candidate, query):
		return 1
	case strings.Contains(candidate, query):
		return 2
	case isSubsequence(query, candidate):
		return 3
	default:
		return -1
	}
}

func isSubsequence(query, candidate string) bool {
	remaining := []rune(query)
	if len(remaining) == 0 {
		return false
	}
	position := 0
	for _, r := range []rune(candidate) {
		if r == remaining[position] {
			position++
			if position == len(remaining) {
				return true
			}
		}
	}
	return false
}

func formatSessionDetail(title string, thread session.ManagedThread) string {
	lines := []string{
		title,
		"名称：" + threadTitle(thread.Info),
		"短编号：" + session.ShortCode(thread.Info.ID),
		"状态：" + formatThreadStatus(thread.Info.Status),
	}
	position := "可用"
	if thread.Archived {
		position = "已归档"
	} else if thread.Current {
		position = "当前"
	}
	lines = append(lines, "位置："+position)
	if thread.Info.IsPinned {
		lines = append(lines, "置顶：是")
	}
	if thread.Info.Cwd != "" {
		lines = append(lines, "目录："+thread.Info.Cwd)
	}
	if preview := sanitizeThreadPreview(thread.Info.Preview); preview != "未命名会话" && preview != threadTitle(thread.Info) {
		lines = append(lines, "摘要："+normalizeSessionLine(preview, 96))
	}
	if thread.Info.CreatedAt > 0 {
		lines = append(lines, "创建："+formatSessionTime(thread.Info.CreatedAt))
	}
	if thread.Info.UpdatedAt > 0 {
		lines = append(lines, "更新："+formatSessionTime(thread.Info.UpdatedAt))
	}
	return strings.Join(lines, "\n")
}

func formatThreadIdentity(thread codex.ThreadInfo) string {
	return fmt.Sprintf("名称：%s\n短编号：%s\n状态：%s", threadTitle(thread), session.ShortCode(thread.ID), formatThreadStatus(thread.Status))
}

func threadTitle(thread codex.ThreadInfo) string {
	return normalizeSessionLine(threadSearchTitle(thread), 60)
}

func threadSearchTitle(thread codex.ThreadInfo) string {
	if name := strings.TrimSpace(thread.Name); name != "" {
		return name
	}
	if preview := strings.TrimSpace(thread.Preview); preview != "" {
		return sanitizeThreadPreview(preview)
	}
	return "未命名会话"
}

func sanitizeThreadPreview(preview string) string {
	preview = strings.ReplaceAll(preview, "\r\n", "\n")
	for _, marker := range []string{"[WeClaw 入站文件]", "[WeClaw 交付物回传]"} {
		if index := strings.Index(preview, marker); index >= 0 {
			preview = preview[:index]
		}
	}
	preview = strings.TrimSpace(preview)
	if index := strings.IndexByte(preview, '\n'); index >= 0 {
		preview = strings.TrimSpace(preview[:index])
	}
	if preview == "" {
		return "未命名会话"
	}
	return preview
}

func normalizeSessionLine(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func formatThreadStatus(status codex.ThreadStatus) string {
	switch status.Type {
	case "active":
		for _, flag := range status.ActiveFlags {
			if flag == "waitingOnApproval" {
				return "等待确认"
			}
		}
		return "执行中"
	case "idle":
		return "空闲"
	case "notLoaded", "":
		return "未加载"
	case "systemError":
		return "异常"
	default:
		return status.Type
	}
}

func formatSessionTime(timestamp int64) string {
	return time.Unix(timestamp, 0).Local().Format("2006-01-02 15:04")
}

func formatSessionError(err error) string {
	switch {
	case errors.Is(err, session.ErrNoActive):
		return "当前没有会话。"
	case errors.Is(err, session.ErrNotOwned):
		return "没有找到属于当前微信用户的会话。"
	case errors.Is(err, session.ErrAmbiguousCode):
		return "会话编号不唯一，请输入更完整的名称或编号。"
	default:
		return fmt.Sprintf("会话操作失败：%v", err)
	}
}
