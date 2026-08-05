package messaging

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/huixiangyang/weclaw/codex"
	"github.com/huixiangyang/weclaw/ilink"
	"github.com/huixiangyang/weclaw/preference"
	"github.com/huixiangyang/weclaw/session"
	"github.com/huixiangyang/weclaw/statefile"
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
	actionTaskMoveFront       controlAction = "task_move_front"
	actionTaskDelete          controlAction = "task_delete"
	actionTaskRetry           controlAction = "task_retry"
	actionTaskFrozenText      controlAction = "task_frozen_text"
	actionQueuePause          controlAction = "queue_pause"
	actionQueueResume         controlAction = "queue_resume"
	actionConfirmQueueClear   controlAction = "confirm_queue_clear"
	actionQueueClear          controlAction = "queue_clear"
	actionRuntimeInfo         controlAction = "runtime_info"
	actionNoReplyDiagnostic   controlAction = "no_reply_diagnostic"
	actionMore                controlAction = "more"
	actionProjectCenter       controlAction = "project_center"
	actionSelectProject       controlAction = "select_project"
	actionProjectQuickTasks   controlAction = "project_quick_tasks"
	actionRunQuickTask        controlAction = "run_quick_task"
	actionLibraryCenter       controlAction = "library_center"
	actionLibraryPage         controlAction = "library_page"
	actionLibraryDetail       controlAction = "library_detail"
	actionResendDelivery      controlAction = "resend_delivery"
	actionRemoteLock          controlAction = "remote_lock"
	actionVoiceBriefing       controlAction = "voice_briefing"
	actionAutomations         controlAction = "automations"
	actionAutomation          controlAction = "automation"
	actionRunAutomation       controlAction = "run_automation"
	actionVisualStyles        controlAction = "visual_styles"
	actionSetVisualStyle      controlAction = "set_visual_style"
	actionResponseModes       controlAction = "response_modes"
	actionSetResponseMode     controlAction = "set_response_mode"
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
	Navigate controlNavigation
}

// controlState 只保存短期微信交互上下文，不承担任务或会话持久化。
type controlState struct {
	Revision  string
	View      controlView
	Mode      controlMode
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
func (h *Handler) handleControlInput(ctx context.Context, userID, text string, hasAttachments bool, sourceKey string) (ActionResult, bool) {
	text = strings.TrimSpace(text)
	registry := h.intents
	if registry == nil {
		registry = mustDefaultIntentRegistry()
	}
	if h.controlStates != nil && strings.TrimSpace(sourceKey) != "" {
		if receipt, exists := h.controlStates.FindReceipt(userID, sourceKey); exists {
			return duplicateControlResult(receipt.ActionID, receipt.Domain), true
		}
	}
	if hasAttachments {
		if !h.deleteControlState(userID) {
			return controlStateFailureResult(), true
		}
		if resolved, ok := registry.Resolve(text); ok && resolved.Definition.AllowAttachments {
			return h.dispatchIntent(ctx, userID, resolved, sourceKey), true
		}
		return ActionResult{}, false
	}

	if resolved, ok := registry.Resolve(text); ok &&
		(resolved.Definition.ID == IntentCancel || resolved.Definition.ID == IntentMenu) {
		return h.dispatchIntent(ctx, userID, resolved, sourceKey), true
	}

	state, status, err := h.loadControlState(userID)
	if err != nil {
		return controlStateFailureResult(), true
	}
	if status == controlStateExpired && isControlContinuation(text) {
		return newActionResult("system.control_expired", DomainSystem, "这个操作已经过期。发送 / 重新打开菜单。"), true
	}
	if status == controlStateActive {
		if result, handled := h.handlePendingControl(ctx, userID, text, state, sourceKey); handled {
			return result, true
		}
	}

	// 旧斜杠命令不再执行，也不转发到 Codex，避免看似成功却走错通道。
	if strings.HasPrefix(text, "/") {
		return newActionResult(
			"system.invalid_slash",
			DomainSystem,
			"斜杠命令已取消。发送一个 / 打开操作菜单，或直接用中文描述操作。",
		), true
	}

	if resolved, ok := registry.Resolve(text); ok {
		return h.dispatchIntent(ctx, userID, resolved, sourceKey), true
	}
	return ActionResult{}, false
}

func (h *Handler) handlePendingControl(ctx context.Context, userID, text string, state *controlState, sourceKey string) (ActionResult, bool) {
	systemResult := func(text string) ActionResult {
		return newActionResult("system.control_input", DomainSystem, text)
	}
	sessionResult := func(actionID, text string) ActionResult {
		return newActionResult(actionID, DomainSession, text)
	}
	consume := func(actionID string, domain ActionDomain, receiptRequired bool, alreadyHandled string) (bool, ActionResult) {
		var ok, duplicate bool
		var err error
		if receiptRequired {
			if h.controlStates == nil {
				return false, controlStateFailureResult()
			}
			if strings.TrimSpace(sourceKey) == "" {
				return false, newActionResult(actionID, domain, "这条微信消息没有稳定来源编号，无法安全执行控制操作。")
			}
			ok, duplicate, err = h.controlStates.ConsumeAndReserve(userID, state.Revision, sourceKey, actionID, domain)
		} else {
			ok, err = h.consumeControlState(userID, state)
		}
		if err != nil {
			logControlStateError(userID, err)
			return false, controlStateFailureResult()
		}
		if duplicate {
			return false, duplicateControlResult(actionID, domain)
		}
		if !ok {
			return false, systemResult(alreadyHandled)
		}
		return true, ActionResult{}
	}

	if isOneOf(text, "返回", "回到菜单") {
		if ok, failure := consume(string(state.Back.Action), controlActionDomain(state.Back.Action), false, "操作状态已经变化。发送 / 重新打开菜单。"); !ok {
			return failure, true
		}
		return h.executeControlAction(ctx, userID, state.Back), true
	}

	switch state.Mode {
	case controlChoice:
		choice, err := strconv.Atoi(text)
		if err != nil {
			if option, ok := controlNavigationOption(text, state.Options); ok {
				if consumed, failure := consume(string(option.Action), controlActionDomain(option.Action), false, "操作状态已经变化。发送 / 重新打开菜单。"); !consumed {
					return failure, true
				}
				return h.executeControlAction(ctx, userID, option), true
			}
			// 非数字内容退出选择态，继续尝试自然语言；普通内容最终进入 Codex。
			if _, consumeErr := h.consumeControlState(userID, state); consumeErr != nil {
				return controlStateFailureResult(), true
			}
			return ActionResult{}, false
		}
		if choice == 0 {
			if consumed, failure := consume(string(state.Back.Action), controlActionDomain(state.Back.Action), false, "操作状态已经变化。发送 / 重新打开菜单。"); !consumed {
				return failure, true
			}
			return h.executeControlAction(ctx, userID, state.Back), true
		}
		if choice < 1 || choice > len(state.Options) {
			return systemResult(fmt.Sprintf("请输入 1-%d，或回复 0 返回。", len(state.Options))), true
		}
		option := state.Options[choice-1]
		if consumed, failure := consume(string(option.Action), controlActionDomain(option.Action), controlActionRequiresReceipt(option.Action), "这个选项已经处理。发送 / 重新打开菜单。"); !consumed {
			return failure, true
		}
		result := h.executeControlAction(ctx, userID, option)
		if result.Effect.Kind == EffectEnqueuePrompt || result.Effect.Kind == EffectRetryTask {
			result = result.withControlRollback(userID, sourceKey, *state)
		}
		return result, true
	case controlNewSessionName:
		if consumed, failure := consume(string(IntentSessionNew), DomainSession, true, "这个操作已经处理。发送 / 重新打开菜单。"); !consumed {
			return failure, true
		}
		if h.hasActiveTask(userID) {
			return sessionResult(string(IntentSessionNew), mutationBusyText()), true
		}
		if text == "0" || isOneOf(text, "跳过", "不命名") {
			return sessionResult(string(IntentSessionNew), h.createSession(ctx, userID, "")), true
		}
		return sessionResult(string(IntentSessionNew), h.createSession(ctx, userID, text)), true
	case controlRenameSession:
		if text == "0" {
			if consumed, failure := consume(string(IntentSessionCenter), DomainSession, false, "这个操作已经处理。发送 / 重新打开菜单。"); !consumed {
				return failure, true
			}
			return sessionResult(string(IntentSessionCenter), h.openSessionMenu(ctx, userID)), true
		}
		if consumed, failure := consume(string(IntentSessionRename), DomainSession, true, "这个操作已经处理。发送 / 重新打开菜单。"); !consumed {
			return failure, true
		}
		if h.hasActiveTask(userID) {
			return sessionResult(string(IntentSessionRename), mutationBusyText()), true
		}
		return sessionResult(string(IntentSessionRename), h.renameSession(ctx, userID, text)), true
	case controlSessionSearch:
		if text == "0" {
			if consumed, failure := consume(string(IntentSessionCenter), DomainSession, false, "这个操作已经处理。发送 / 重新打开菜单。"); !consumed {
				return failure, true
			}
			return sessionResult(string(IntentSessionCenter), h.openSessionMenu(ctx, userID)), true
		}
		if consumed, failure := consume(string(IntentSessionSearch), DomainSession, false, "这个操作已经处理。发送 / 重新打开菜单。"); !consumed {
			return failure, true
		}
		return sessionResult(string(IntentSessionSearch), h.openSessionBrowser(ctx, userID, false, text)), true
	default:
		if !h.deleteControlState(userID) {
			return controlStateFailureResult(), true
		}
		return ActionResult{}, false
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
	running := h.hasActiveTask(userID)
	if running {
		taskState = "运行中"
	}
	statuses := h.automationStatuses(userID)
	var options []controlOption
	if running {
		options = append(options,
			controlOption{Label: "任务状态", Action: actionTaskStatus},
			controlOption{Label: "任务中心", Action: actionActivityPage, Page: 1},
			controlOption{Label: "当前会话", Action: actionCurrentSession},
		)
	} else {
		options = append(options,
			controlOption{Label: "项目", Action: actionProjectCenter},
			controlOption{Label: "会话", Action: actionSessionMenu},
			controlOption{Label: "任务中心", Action: actionActivityPage, Page: 1},
		)
	}
	options = append(options, controlOption{Label: "更多功能", Action: actionMore})
	projectName := "未配置"
	if h.projects != nil {
		projectName = h.projects.Current(userID).Name
	}
	lines := []string{
		"WeClaw",
		"",
		"版本：" + h.bridgeVersion,
		"项目：" + projectName,
		"会话：" + currentName,
		"状态：" + taskState,
		"回答：" + h.currentResponseMode(userID).Definition().Name,
	}
	if h.tasks != nil {
		status := h.tasks.Status(userID)
		lines = append(lines, fmt.Sprintf("队列：%d 项等待", status.Queued))
	}
	if len(statuses) > 0 {
		lines = append(lines, fmt.Sprintf("自动化：%d 项", len(statuses)))
	}
	lines = append(lines, "", renderControlOptions(options))
	prompt := strings.Join(lines, "\n")
	if !h.storeChoice(userID, viewSystemMain, options, actionExit) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字即可，0 退出。"
}

func (h *Handler) openTaskStatus(userID string) string {
	options := []controlOption{{Label: "刷新状态", Action: actionTaskStatus}}
	if h.hasActiveTask(userID) {
		options = append(options,
			controlOption{Label: "任务中心", Action: actionActivityPage, Page: 1},
			controlOption{Label: "取消当前任务", Action: actionConfirmCancelTask},
		)
	} else {
		options = append(options, controlOption{Label: "运行中心", Action: actionRuntimeInfo})
	}
	options = append(options, controlOption{Label: "任务中心", Action: actionActivityPage, Page: 1})
	prompt := h.buildTaskStatus(userID) + "\n\n" + renderControlOptions(options)
	if !h.storeChoice(userID, viewTaskStatus, options, actionMain) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字操作，0 返回。"
}

func (h *Handler) confirmCancelTask(userID string) string {
	if !h.hasActiveTask(userID) {
		return h.openTaskStatus(userID)
	}
	options := []controlOption{{Label: "确认取消任务", Action: actionCancelTask}}
	prompt := "准备取消当前任务\n\n取消后，本次迟到的进度和最终结果都不会发送。\n\n" + renderControlOptions(options)
	if !h.storeChoice(userID, viewTaskCancelConfirm, options, actionTaskStatus) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复 1 确认，0 返回任务状态。"
}

func (h *Handler) openRuntimeInfo(userID string) string {
	options := []controlOption{
		{Label: "为什么没回复", Action: actionNoReplyDiagnostic},
		{Label: "项目中心", Action: actionProjectCenter},
		{Label: "刷新运行中心", Action: actionRuntimeInfo},
	}
	prompt := h.buildStatus() + "\n\n" + renderControlOptions(options)
	if !h.storeChoice(userID, viewSystemRuntime, options, actionMain) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字操作，0 返回。"
}

func (h *Handler) openMoreMenu(userID string) string {
	options := []controlOption{
		{Label: "运行中心", Action: actionRuntimeInfo},
		{Label: "自动化中心", Action: actionAutomations, Page: 1},
		{Label: "素材与交付", Action: actionLibraryCenter},
		{Label: "远程锁定", Action: actionRemoteLock},
	}
	if h.preferences != nil {
		options = append(options, controlOption{Label: "偏好设置", Action: actionResponseModes})
	}
	if h.voice != nil {
		options = append(options, controlOption{Label: "语音简报", Action: actionVoiceBriefing})
	}
	options = append(options, controlOption{Label: "使用说明", Action: actionGuide})
	prompt := "更多功能\n\n" + renderControlOptions(options)
	if !h.storeChoice(userID, viewSystemMore, options, actionMain) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字操作，0 返回。"
}

func (h *Handler) openGuide(userID string) string {
	options := []controlOption{
		{Label: "会话中心", Action: actionSessionMenu},
		{Label: "任务状态", Action: actionTaskStatus},
	}
	if h.preferences != nil {
		options = append(options, controlOption{Label: "偏好设置", Action: actionResponseModes})
	}
	prompt := "使用说明\n\n" + controlGuide() + "\n\n" + renderControlOptions(options)
	if !h.storeChoice(userID, viewSystemGuide, options, actionMain) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字继续，0 返回。"
}

func (h *Handler) openResponseModes(userID string) string {
	if h.preferences == nil {
		return "回答偏好当前不可用。"
	}
	current := h.preferences.Get(userID)
	options := make([]controlOption, 0, 5)
	for _, definition := range preference.ResponseModes() {
		if definition.ID == preference.ResponseReading && h.visual == nil {
			continue
		}
		if definition.ID == preference.ResponseVoice && (h.visual == nil || h.voice == nil) {
			continue
		}
		options = append(options, controlOption{
			Label:  definition.Name + " · " + definition.Description,
			Action: actionSetResponseMode,
			Value:  string(definition.ID),
		})
	}
	if h.visual != nil {
		options = append(options, controlOption{Label: "视觉风格", Action: actionVisualStyles})
	}
	if h.voice != nil {
		options = append(options, controlOption{Label: "立即发送语音简报", Action: actionVoiceBriefing})
	}
	styleName := current.Style.Definition().Name
	prompt := strings.Join([]string{
		"回答方式",
		"",
		"当前：" + current.ResponseMode.Definition().Name,
		"视觉：" + styleName,
		"说明：语音模式会把 Codex 回答整理为阅读卡和 MP3；长回答同时保留完整阅读卡。",
		"",
		renderControlOptions(options),
	}, "\n")
	if !h.storeChoice(userID, viewPreferenceResponse, options, actionMain) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字切换，0 返回。"
}

func (h *Handler) setResponseMode(userID string, mode preference.ResponseMode) string {
	if h.preferences == nil || !mode.Valid() {
		return "回答方式当前不可用。"
	}
	if mode == preference.ResponseReading && h.visual == nil {
		return "阅读模式需要启用视觉卡片。"
	}
	if mode == preference.ResponseVoice && (h.visual == nil || h.voice == nil) {
		return "语音模式需要同时启用视觉卡片和语音提供商。"
	}
	if err := h.preferences.SetResponseMode(userID, mode); err != nil {
		log.Printf("[preference] failed to persist response mode for %s: %v", ilink.LogLabel(userID), err)
		return "回答方式保存失败，请稍后重试。"
	}
	definition := mode.Definition()
	options := []controlOption{
		{Label: "选择其他回答方式", Action: actionResponseModes},
		{Label: "返回主菜单", Action: actionMain},
	}
	prompt := strings.Join([]string{
		"回答方式已切换",
		"",
		"当前：" + definition.Name,
		"说明：" + definition.Description,
		"",
		renderControlOptions(options),
	}, "\n")
	if !h.storeChoice(userID, viewPreferenceResponse, options, actionMain) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字继续，0 返回。"
}

func (h *Handler) openVisualStyles(userID string) string {
	if h.visual == nil || h.preferences == nil {
		return "视觉卡片当前不可用。"
	}
	current := h.preferences.Get(userID).Style
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
	if !h.storeChoice(userID, viewPreferenceVisual, options, actionResponseModes) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字切换并查看效果，0 返回。"
}

func (h *Handler) setVisualStyle(userID string, style visual.Style) string {
	if h.visual == nil || h.preferences == nil {
		return "视觉卡片当前不可用。"
	}
	if !style.Valid() {
		return "没有这个视觉风格。发送“视觉风格”查看可选模板。"
	}
	if err := h.preferences.SetStyle(userID, style); err != nil {
		log.Printf("[visual] failed to persist style for %s: %v", ilink.LogLabel(userID), err)
		return "视觉风格保存失败，请稍后重试。"
	}
	definition := style.Definition()
	options := []controlOption{
		{Label: "选择其他风格", Action: actionVisualStyles},
		{Label: "返回偏好设置", Action: actionResponseModes},
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
	if !h.storeChoice(userID, viewPreferenceVisual, options, actionResponseModes) {
		return controlStateFailureResult().Text
	}
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
	if !h.storeChoice(userID, viewSessionCenter, options, actionMain) {
		return controlStateFailureResult().Text
	}
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
			if !h.storeChoice(userID, viewSessionCurrent, options, actionSessionMenu) {
				return controlStateFailureResult().Text
			}
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
	if !h.storeChoice(userID, viewSessionCurrent, options, actionSessionMenu) {
		return controlStateFailureResult().Text
	}
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
	if !h.storeChoice(userID, viewSessionList, options, actionSessionMenu) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字，或直接说“下一页”“上一页”；0 返回。"
}

func (h *Handler) promptSessionSearch(userID string) string {
	prompt := "搜索会话\n\n发送名称、短编号或记得的连续字符，回复 0 返回。"
	if !h.storeInput(userID, viewSessionSearchInput, controlSessionSearch, actionSessionMenu) {
		return controlStateFailureResult().Text
	}
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
	if !h.storeChoiceWithBack(userID, viewSessionDetail, options, back) {
		return controlStateFailureResult().Text
	}
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
	if !h.storeChoiceWithBack(userID, viewSessionArchive, options, back) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复 1 确认，0 返回会话详情。"
}

func (h *Handler) archiveSession(ctx context.Context, userID, threadID string) string {
	return h.withRuntimeMutation(func() string { return h.archiveSessionUnlocked(ctx, userID, threadID) })
}

func (h *Handler) archiveSessionUnlocked(ctx context.Context, userID, threadID string) string {
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
	if !h.storeChoice(userID, viewSessionResult, options, actionSessionMenu) {
		return controlStateFailureResult().Text
	}
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
	if !h.storeInput(userID, viewSessionNewInput, controlNewSessionName, actionSessionMenu) {
		return controlStateFailureResult().Text
	}
	return prompt
}

func (h *Handler) promptRenameSession(userID string) string {
	prompt := "重命名会话\n\n发送新的会话名称，回复 0 返回。"
	if !h.storeInput(userID, viewSessionRenameInput, controlRenameSession, actionSessionMenu) {
		return controlStateFailureResult().Text
	}
	return prompt
}

func (h *Handler) createSession(ctx context.Context, userID, name string) string {
	return h.withRuntimeMutation(func() string {
		threadAgent, err := h.sessionContext()
		if err != nil {
			return err.Error()
		}
		if h.projects != nil {
			h.codex.SetCwd(h.projects.Current(userID).Root)
		}
		thread, err := h.sessions.New(ctx, userID, threadAgent, strings.TrimSpace(name))
		if err != nil {
			return formatSessionError(err)
		}
		return h.sessionSuccess(userID, "已创建并切换到新会话。", thread)
	})
}

func (h *Handler) useSession(ctx context.Context, userID, threadID string) string {
	return h.withRuntimeMutation(func() string {
		threadAgent, err := h.sessionContext()
		if err != nil {
			return err.Error()
		}
		thread, err := h.sessions.Use(ctx, userID, threadAgent, threadID)
		if err != nil {
			return formatSessionError(err)
		}
		return h.sessionSuccess(userID, "已切换会话。", thread)
	})
}

func (h *Handler) renameSession(ctx context.Context, userID, name string) string {
	return h.withRuntimeMutation(func() string {
		threadAgent, err := h.sessionContext()
		if err != nil {
			return err.Error()
		}
		thread, err := h.sessions.Rename(ctx, userID, threadAgent, strings.TrimSpace(name))
		if err != nil {
			return formatSessionError(err)
		}
		return h.sessionSuccess(userID, "会话已重命名。", thread)
	})
}

func (h *Handler) withRuntimeMutation(action func() string) string {
	if h.coordinator == nil {
		return action()
	}
	result := ""
	if !h.coordinator.TryRuntimeControl(func() { result = action() }) {
		return "Codex 正在执行另一项任务，会话修改暂不可用。项目切换和新任务排队不受影响。"
	}
	return result
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
	if !h.storeChoice(userID, viewSessionArchive, options, actionSessionMenu) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复 1 确认，0 返回。"
}

func (h *Handler) archiveCurrentSession(ctx context.Context, userID string) string {
	return h.withRuntimeMutation(func() string { return h.archiveCurrentSessionUnlocked(ctx, userID) })
}

func (h *Handler) archiveCurrentSessionUnlocked(ctx context.Context, userID string) string {
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
		if !h.storeChoice(userID, viewSessionResult, options, actionSessionMenu) {
			return controlStateFailureResult().Text
		}
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
	if !h.storeChoice(userID, viewSessionResult, options, actionSessionMenu) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字继续，0 返回会话中心。"
}

func (h *Handler) restoreSession(ctx context.Context, userID, threadID string) string {
	return h.withRuntimeMutation(func() string { return h.restoreSessionUnlocked(ctx, userID, threadID) })
}

func (h *Handler) restoreSessionUnlocked(ctx context.Context, userID, threadID string) string {
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
	if !h.storeChoice(userID, viewSessionResult, options, actionSessionMenu) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字继续，0 返回会话中心。"
}

func (h *Handler) sessionSuccess(userID, headline string, thread codex.ThreadInfo) string {
	options := []controlOption{
		{Label: "查看当前会话", Action: actionCurrentSession},
		{Label: "会话列表", Action: actionBrowseSessions},
		{Label: "会话中心", Action: actionSessionMenu},
	}
	prompt := headline + "\n" + formatThreadIdentity(thread) + "\n\n" + renderControlOptions(options)
	if !h.storeChoice(userID, viewSessionResult, options, actionSessionMenu) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字继续，或直接发送内容开始对话；0 返回会话中心。"
}

func (h *Handler) storeChoice(userID string, view controlView, options []controlOption, back controlAction) bool {
	return h.storeChoiceWithBack(userID, view, options, controlOption{Action: back})
}

// storeChoiceWithBack 为分页详情保留完整返回位置，避免移动端反复翻页。
func (h *Handler) storeChoiceWithBack(userID string, view controlView, options []controlOption, back controlOption) bool {
	if h.controlStates == nil {
		log.Printf("[control] persistent state store is unavailable for %s", ilink.LogLabel(userID))
		return false
	}
	state := controlState{
		View: view,
		Mode: controlChoice, Options: append([]controlOption(nil), options...),
		Back: back, ExpiresAt: time.Now().Add(controlStateTTL),
	}
	if _, err := h.controlStates.Put(userID, state); err != nil {
		logControlStateError(userID, err)
		return false
	}
	return true
}

func (h *Handler) storeInput(userID string, view controlView, mode controlMode, back controlAction) bool {
	if h.controlStates == nil {
		log.Printf("[control] persistent state store is unavailable for %s", ilink.LogLabel(userID))
		return false
	}
	state := controlState{
		View: view,
		Mode: mode, Back: controlOption{Action: back}, ExpiresAt: time.Now().Add(controlStateTTL),
	}
	if _, err := h.controlStates.Put(userID, state); err != nil {
		logControlStateError(userID, err)
		return false
	}
	return true
}

func (h *Handler) loadControlState(userID string) (*controlState, controlStateStatus, error) {
	if h.controlStates == nil {
		return nil, controlStateMissing, nil
	}
	return h.controlStates.Load(userID)
}

func (h *Handler) consumeControlState(userID string, state *controlState) (bool, error) {
	if h.controlStates == nil || state == nil {
		return false, nil
	}
	deleted, err := h.controlStates.CompareAndDelete(userID, state.Revision)
	if err != nil {
		logControlStateError(userID, err)
	}
	return deleted, err
}

func (h *Handler) deleteControlState(userID string) bool {
	if h.controlStates == nil {
		return true
	}
	if err := h.controlStates.Delete(userID); err != nil {
		logControlStateError(userID, err)
		return false
	}
	return true
}

func logControlStateError(userID string, err error) {
	log.Printf("[control] persistent state failed category=%s for %s", statefile.ErrorCategory(err), ilink.LogLabel(userID))
}

func controlStateFailureResult() ActionResult {
	return newActionResult("system.control_unavailable", DomainSystem, "操作状态暂不可用。请稍后重新发送 /。")
}

func isControlContinuation(text string) bool {
	text = strings.TrimSpace(text)
	if _, err := strconv.Atoi(text); err == nil {
		return true
	}
	return isOneOf(text, "返回", "回到菜单", "下一页", "下页", "往后", "后面", "上一页", "上页", "往前", "前面")
}

func (h *Handler) hasActiveTask(userID string) bool {
	if h.tasks == nil {
		return false
	}
	status := h.tasks.Status(userID)
	return status.Running > 0 || status.Delivering > 0
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
		if option.Action != actionSessionPage && option.Action != actionAutomations && option.Action != actionActivityPage {
			continue
		}
		if forward && option.Navigate == navigationNext {
			return option, true
		}
		if backward && option.Navigate == navigationPrevious {
			return option, true
		}
	}
	return controlOption{}, false
}

func controlGuide() string {
	return strings.Join([]string{
		"直接发送文字、图片或文件，内容会交给 Codex。",
		"发送“回答方式”可选择自适应、阅读或语音；语音会配套发送阅读卡和 MP3。",
		"阅读卡回复支持发送“文字版”获取可复制原文。",
		"发送 / 打开操作菜单，回复数字或“下一页”“上一页”完成选择。",
		"发送“视觉风格”可在五套完整模板间切换，选择会自动保存。",
		"也可以直接说“切换项目”“新建会话”“搜索会话”“切换会话 登录”或“运行中心”。",
		"任务运行时发送“状态”查看进度，发送“取消”停止任务。",
	}, "\n")
}

func mutationBusyText() string {
	return "当前任务仍在运行，暂时不能切换项目或修改会话。发送“状态”查看进度，或发送“取消”停止任务。"
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
