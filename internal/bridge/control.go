package bridge

import (
	"context"
	"errors"
	"fmt"
	"github.com/huixiangyang/codex-link-clawbot/internal/control"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
	"github.com/huixiangyang/codex-link-clawbot/internal/thread"
)

const (
	controlStateTTL        = 10 * time.Minute
	controlDirectoryTTL    = 30 * time.Minute
	controlWorkbenchTTL    = 5 * time.Minute
	controlSessionPageSize = 6
)

type controlMode uint8

const (
	controlChoice controlMode = iota + 1
	controlNewSessionName
	controlRenameSession
	controlSessionSearch
	controlGlobalThreadSearch
	controlThreadGoal
)

type controlAction string

const (
	actionExit                  controlAction = "exit"
	actionMain                  controlAction = "main"
	actionFunctionDirectory     controlAction = "function_directory"
	actionSessionMenu           controlAction = "thread_menu"
	actionCodexDevelopment      controlAction = "codex_development"
	actionCodexCommands         controlAction = "codex_commands"
	actionCodexCommandPage      controlAction = "codex_command_page"
	actionCodexCommand          controlAction = "codex_command"
	actionCodexUsage            controlAction = "codex_usage"
	actionCodexPermissions      controlAction = "codex_permissions"
	actionCodexGoalStatus       controlAction = "codex_goal_status"
	actionCodexGlobalOverview   controlAction = "codex_global_overview"
	actionCodexGlobalThreadPage controlAction = "codex_global_thread_page"
	actionCodexUseGlobalThread  controlAction = "codex_use_global_thread"
	actionCodexAccount          controlAction = "codex_account"
	actionCodexModelOverview    controlAction = "codex_model_overview"
	actionPromptGlobalSearch    controlAction = "codex_global_search_prompt"
	actionCurrentSession        controlAction = "current_thread"
	actionThreadRelations       controlAction = "thread_relations"
	actionPickSession           controlAction = "pick_thread"
	actionBrowseSessions        controlAction = "browse_threads"
	actionPromptSessionSearch   controlAction = "prompt_thread_search"
	actionSessionPage           controlAction = "thread_page"
	actionSessionDetail         controlAction = "thread_detail"
	actionUseSession            controlAction = "use_thread"
	actionPromptNewSession      controlAction = "prompt_new_thread"
	actionPromptRenameSession   controlAction = "prompt_rename_thread"
	actionConfirmArchive        controlAction = "confirm_archive_thread"
	actionArchiveCurrent        controlAction = "archive_current_thread"
	actionConfirmArchiveItem    controlAction = "confirm_archive_thread_item"
	actionArchiveItem           controlAction = "archive_thread_item"
	actionPickArchivedSession   controlAction = "pick_archived_thread"
	actionRestoreSession        controlAction = "restore_thread"
	actionForkThread            controlAction = "thread_fork"
	actionToggleThreadPin       controlAction = "thread_toggle_pin"
	actionCompactThread         controlAction = "thread_compact"
	actionPromptThreadGoal      controlAction = "thread_goal_prompt"
	actionClearThreadGoal       controlAction = "thread_goal_clear"
	actionPauseThreadGoal       controlAction = "thread_goal_pause"
	actionResumeThreadGoal      controlAction = "thread_goal_resume"
	actionReviewThread          controlAction = "thread_review"
	actionReviewContinue        controlAction = "thread_review_continue"
	actionReviewAccept          controlAction = "thread_review_accept"
	actionReviewRerun           controlAction = "thread_review_rerun"
	actionCodexCapabilities     controlAction = "codex_capabilities"
	actionConfirmDeleteThread   controlAction = "thread_delete_confirm"
	actionDeleteThread          controlAction = "thread_delete"
	actionThreadModels          controlAction = "thread_models"
	actionSelectThreadModel     controlAction = "thread_model_select"
	actionThreadEfforts         controlAction = "thread_efforts"
	actionSelectThreadEffort    controlAction = "thread_effort_select"
	actionTaskStatus            controlAction = "queue_status"
	actionConfirmCancelTask     controlAction = "confirm_cancel_queue_execution"
	actionCancelTask            controlAction = "cancel_queue_execution"
	actionActivityPage          controlAction = "queue_page"
	actionActivityDetail        controlAction = "queue_detail"
	actionTaskMoveFront         controlAction = "queue_move_front"
	actionTaskDelete            controlAction = "queue_delete"
	actionTaskRetry             controlAction = "queue_retry"
	actionTaskContinueSession   controlAction = "queue_continue_in_thread"
	actionTaskRerun             controlAction = "queue_rerun"
	actionTaskRerunNewSession   controlAction = "queue_continue_in_new_thread"
	actionTaskFrozenText        controlAction = "queue_frozen_text"
	actionRecentResult          controlAction = "recent_result"
	actionQueuePause            controlAction = "queue_pause"
	actionQueueResume           controlAction = "queue_resume"
	actionConfirmQueueClear     controlAction = "confirm_queue_clear"
	actionQueueClear            controlAction = "queue_clear"
	actionRuntimeInfo           controlAction = "runtime_info"
	actionNoReplyDiagnostic     controlAction = "no_reply_diagnostic"
	actionResultsDeliveryCenter controlAction = "results_delivery_center"
	actionSettingsCenter        controlAction = "settings_center"
	actionConfigurationStatus   controlAction = "configuration_status"
	actionDiagnosticsCenter     controlAction = "diagnostics_center"
	actionProjectCenter         controlAction = "project_center"
	actionSelectProject         controlAction = "select_project"
	actionDeliveryBox           controlAction = "delivery_box"
	actionDeliveryPage          controlAction = "delivery_page"
	actionDeliveryDetail        controlAction = "delivery_detail"
	actionResendDelivery        controlAction = "resend_delivery"
	actionRemoteLock            controlAction = "remote_lock"
	actionConfirmRemoteLock     controlAction = "confirm_remote_lock"
	actionVoiceBriefing         controlAction = "voice_briefing"
	actionVisualStyles          controlAction = "visual_styles"
	actionSetVisualStyle        controlAction = "set_visual_style"
	actionResponseModes         controlAction = "response_modes"
	actionSetResponseMode       controlAction = "set_response_mode"
	actionGuide                 controlAction = "guide"
)

type controlOption struct {
	Code     string
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
	Item  thread.ManagedThread
	Score int
}

// handleControlInput 解析菜单、数字回复和高置信度中文控制意图。
// 返回 handled=false 时，原始消息必须原样交给 Codex，不能静默吞掉。
func (h *Handler) handleControlInput(ctx context.Context, userID, text string, hasAttachments bool, sourceKey string) (ActionResult, bool) {
	text = strings.TrimSpace(text)
	registry := h.intents
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
		(resolved.Definition.ID == control.IntentCancel || resolved.Definition.ID == control.IntentMenu) {
		return h.dispatchIntent(ctx, userID, resolved, sourceKey), true
	}

	state, status, err := h.loadControlState(userID)
	if err != nil {
		return controlStateFailureResult(), true
	}
	if status == controlStateExpired && isControlContinuation(text) {
		return newActionResult("system.control_expired", control.DomainSystem, "这个操作已经过期。发送“菜单”重新打开操作总览。"), true
	}
	if status == controlStateActive {
		if result, handled := h.handlePendingControl(ctx, userID, text, state, sourceKey); handled {
			return result, true
		}
	}

	// 微信控制面只接受菜单编号；斜杠语法既不执行，也不下沉为 Codex 提示词。
	if strings.HasPrefix(text, "/") {
		return newActionResult(
			"system.invalid_command_input",
			control.DomainSystem,
			"微信端不接受斜杠命令。发送“菜单”打开操作总览，再回复图片中的数字编号。",
		), true
	}

	if resolved, ok := registry.Resolve(text); ok {
		return h.dispatchIntent(ctx, userID, resolved, sourceKey), true
	}
	return ActionResult{}, false
}

func (h *Handler) handlePendingControl(ctx context.Context, userID, text string, state *controlState, sourceKey string) (ActionResult, bool) {
	systemResult := func(text string) ActionResult {
		return newActionResult("system.control_input", control.DomainSystem, text)
	}
	sessionResult := func(actionID, text string) ActionResult {
		return newActionResult(actionID, control.DomainSession, text)
	}
	consume := func(actionID string, domain control.Domain, receiptRequired bool, alreadyHandled string) (bool, ActionResult) {
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
		if ok, failure := consume(string(state.Back.Action), controlActionDomain(state.Back.Action), false, "操作状态已经变化。发送“菜单”重新打开操作总览。"); !ok {
			return failure, true
		}
		return h.executeControlAction(ctx, userID, state.Back), true
	}

	switch state.Mode {
	case controlChoice:
		if _, err := strconv.Atoi(text); err != nil {
			if option, ok := controlNavigationOption(text, state.Options); ok {
				if consumed, failure := consume(string(option.Action), controlActionDomain(option.Action), false, "操作状态已经变化。发送“菜单”重新打开操作总览。"); !consumed {
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
		if text == "0" {
			if consumed, failure := consume(string(state.Back.Action), controlActionDomain(state.Back.Action), false, "操作状态已经变化。发送“菜单”重新打开操作总览。"); !consumed {
				return failure, true
			}
			return h.executeControlAction(ctx, userID, state.Back), true
		}
		option, exists := controlOptionByCode(text, state.Options)
		if !exists {
			return systemResult("这个编号不在当前菜单中。请回复图片中的编号，或回复 0 返回。"), true
		}
		if consumed, failure := consume(string(option.Action), controlActionDomain(option.Action), controlActionRequiresReceipt(option.Action), "这个选项已经处理。发送“菜单”重新打开操作总览。"); !consumed {
			return failure, true
		}
		result := h.executeControlAction(ctx, userID, option)
		if result.Effect.Kind == EffectEnqueuePrompt || result.Effect.Kind == EffectRetryTask {
			result = result.withControlRollback(userID, sourceKey, *state)
		}
		return result, true
	case controlNewSessionName:
		if consumed, failure := consume(string(control.IntentSessionNew), control.DomainSession, true, "这个操作已经处理。发送“菜单”重新打开操作总览。"); !consumed {
			return failure, true
		}
		if h.hasActiveTask(userID) {
			return sessionResult(string(control.IntentSessionNew), mutationBusyText()), true
		}
		if text == "0" || isOneOf(text, "跳过", "不命名") {
			return sessionResult(string(control.IntentSessionNew), h.createSession(ctx, userID, "")), true
		}
		return sessionResult(string(control.IntentSessionNew), h.createSession(ctx, userID, text)), true
	case controlRenameSession:
		if text == "0" {
			if consumed, failure := consume(string(control.IntentSessionCenter), control.DomainSession, false, "这个操作已经处理。发送“菜单”重新打开操作总览。"); !consumed {
				return failure, true
			}
			return sessionResult(string(control.IntentSessionCenter), h.openSessionMenu(ctx, userID)), true
		}
		if consumed, failure := consume(string(control.IntentSessionRename), control.DomainSession, true, "这个操作已经处理。发送“菜单”重新打开操作总览。"); !consumed {
			return failure, true
		}
		if h.hasActiveTask(userID) {
			return sessionResult(string(control.IntentSessionRename), mutationBusyText()), true
		}
		return sessionResult(string(control.IntentSessionRename), h.renameSession(ctx, userID, text)), true
	case controlSessionSearch:
		if text == "0" {
			if consumed, failure := consume(string(control.IntentSessionCenter), control.DomainSession, false, "这个操作已经处理。发送“菜单”重新打开操作总览。"); !consumed {
				return failure, true
			}
			return sessionResult(string(control.IntentSessionCenter), h.openSessionMenu(ctx, userID)), true
		}
		if consumed, failure := consume(string(control.IntentSessionSearch), control.DomainSession, false, "这个操作已经处理。发送“菜单”重新打开操作总览。"); !consumed {
			return failure, true
		}
		return sessionResult(string(control.IntentSessionSearch), h.openSessionBrowser(ctx, userID, false, text)), true
	case controlGlobalThreadSearch:
		if text == "0" {
			if consumed, failure := consume(string(actionCodexGlobalOverview), control.DomainSession, false, "这个操作已经处理。发送“菜单”重新打开操作总览。"); !consumed {
				return failure, true
			}
			return sessionResult(string(actionCodexGlobalOverview), h.openCodexGlobalOverview(ctx, userID)), true
		}
		if consumed, failure := consume(string(actionPromptGlobalSearch), control.DomainSession, false, "这个操作已经处理。发送“菜单”重新打开操作总览。"); !consumed {
			return failure, true
		}
		return sessionResult(string(actionCodexGlobalThreadPage), h.openCodexGlobalThreadPage(ctx, userID, false, false, text, 1)), true
	case controlThreadGoal:
		if text == "0" {
			if consumed, failure := consume(string(actionSessionMenu), control.DomainSession, false, "这个操作已经处理。发送“菜单”重新打开操作总览。"); !consumed {
				return failure, true
			}
			return sessionResult(string(actionSessionMenu), h.openSessionMenu(ctx, userID)), true
		}
		if consumed, failure := consume(string(actionPromptThreadGoal), control.DomainSession, true, "这个操作已经处理。发送“菜单”重新打开操作总览。"); !consumed {
			return failure, true
		}
		return sessionResult(string(actionPromptThreadGoal), h.setCurrentThreadGoal(ctx, userID, text)), true
	default:
		if !h.deleteControlState(userID) {
			return controlStateFailureResult(), true
		}
		return ActionResult{}, false
	}
}

func (h *Handler) openMainMenu(ctx context.Context, userID string) string {
	return h.openMainMenuPage(ctx, userID).Text
}

func (h *Handler) openMainMenuPage(ctx context.Context, userID string) controlPage {
	return h.buildGlobalWorkbench(ctx, userID)
}

func (h *Handler) openTaskStatus(userID string) string {
	options := []controlOption{
		{Label: "刷新状态", Action: actionTaskStatus},
		{Label: "codex-link-clawbot 请求队列", Action: actionActivityPage, Page: 1},
	}
	if h.hasActiveTask(userID) {
		options = append(options, controlOption{Label: "取消当前执行", Action: actionConfirmCancelTask})
	} else {
		options = append(options, controlOption{Label: "codex-link-clawbot 运行状态", Action: actionRuntimeInfo})
	}
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
	options := []controlOption{{Label: "确认取消执行", Action: actionCancelTask}}
	prompt := "准备取消 codex-link-clawbot 当前执行\n\n如果 Codex 轮次已经启动，codex-link-clawbot 会请求中断；本次迟到的进度和最终结果都不会发送。\n\n" + renderControlOptions(options)
	if !h.storeChoice(userID, viewTaskCancelConfirm, options, actionTaskStatus) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复 1 确认，0 返回执行状态。"
}

func (h *Handler) openRuntimeInfo(userID string) string {
	options := []controlOption{
		{Label: "为什么没回复", Action: actionNoReplyDiagnostic},
		{Label: "有效配置状态", Action: actionConfigurationStatus},
		{Label: "刷新 codex-link-clawbot 状态", Action: actionRuntimeInfo},
	}
	prompt := h.buildStatus(userID) + "\n\n" + renderControlOptions(options)
	if !h.storeChoice(userID, viewSystemRuntime, options, actionDiagnosticsCenter) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字操作，0 返回。"
}

func (h *Handler) openResultsDeliveryCenter(userID string) string {
	deliveryCount := 0
	if h.deliveries != nil {
		deliveryCount = len(h.deliveries.List(userID))
	}
	options := []controlOption{
		{Label: "最近成功结果", Action: actionRecentResult},
		{Label: fmt.Sprintf("交付箱 · %d 项", deliveryCount), Action: actionDeliveryBox},
	}
	if h.voice != nil {
		options = append(options, controlOption{Label: "播报最近成功结果", Action: actionVoiceBriefing})
	}
	prompt := strings.Join([]string{
		"最近结果与交付箱", "",
		"从最近一次成功执行继续工作，或重新发送已归档的交付文件。", "",
		renderControlOptions(options),
	}, "\n")
	if !h.storeChoice(userID, viewSystemResults, options, actionMain) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字操作，0 返回。"
}

func (h *Handler) openSettingsCenter(userID string) string {
	options := []controlOption{
		{Label: "回复方式与视觉", Action: actionResponseModes},
	}
	if h.remoteLock != nil && h.remoteLock.Enabled() {
		options = append(options, controlOption{Label: "远程锁定", Action: actionConfirmRemoteLock})
	}
	prompt := strings.Join([]string{
		"呈现与安全", "",
		"微信端只管理个人回复呈现和远程锁定；机器级目录、命令与密钥只允许在本机修改。", "",
		renderControlOptions(options),
	}, "\n")
	if !h.storeChoice(userID, viewSystemSettings, options, actionMain) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字操作，0 返回。"
}

func (h *Handler) openConfigurationStatus(userID string) string {
	currentProject := "未配置"
	projectCount := 0
	if h.projects != nil {
		currentProject = h.projects.Current(userID).Name
		projectCount = len(h.projects.List())
	}
	preference := h.currentResponseMode(userID)
	style := h.currentVisualStyle(userID)
	options := []controlOption{
		{Label: "修改回复偏好", Action: actionResponseModes},
		{Label: "返回呈现与安全", Action: actionSettingsCenter},
	}
	prompt := strings.Join([]string{
		"codex-link-clawbot 有效配置", "",
		fmt.Sprintf("项目入口：%s · 共 %d 项", currentProject, projectCount),
		"回答方式：" + preference.Definition().Name,
		"视觉风格：" + style.Definition().Name,
		"视觉渲染：" + enabledText(h.visual != nil),
		"语音交付：" + enabledText(h.voice != nil),
		"进度提示：" + enabledText(h.progress.Enabled),
		"远程锁定：" + enabledText(h.remoteLock != nil && h.remoteLock.Enabled()),
		"", "机器级配置只读。本机执行 codex-link-clawbot config 查看脱敏后的完整状态。", "",
		renderControlOptions(options),
	}, "\n")
	if !h.storeChoice(userID, viewSystemSettings, options, actionSettingsCenter) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字操作，0 返回呈现与安全。"
}

func (h *Handler) openDiagnosticsCenter(userID string) string {
	options := []controlOption{
		{Label: "为什么没回复", Action: actionNoReplyDiagnostic},
		{Label: "运行状态", Action: actionRuntimeInfo},
		{Label: "有效配置状态", Action: actionConfigurationStatus},
		{Label: "使用说明", Action: actionGuide},
	}
	prompt := "codex-link-clawbot 诊断中心\n\n故障判断只读取确定性运行状态，不把诊断问题交给 Codex。\n\n" + renderControlOptions(options)
	if !h.storeChoice(userID, viewSystemDiagnostics, options, actionMain) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字操作，0 返回。"
}

func enabledText(enabled bool) string {
	if enabled {
		return "已启用"
	}
	return "未启用"
}

func (h *Handler) openGuide(userID string) string {
	options := []controlOption{
		{Label: "Codex 线程", Action: actionSessionMenu},
		{Label: "codex-link-clawbot 执行状态", Action: actionTaskStatus},
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
	for _, definition := range presentation.ResponseModes() {
		if definition.ID == presentation.ResponseReading && h.visual == nil {
			continue
		}
		if definition.ID == presentation.ResponseVoice && (h.visual == nil || h.voice == nil) {
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
	styleName := current.Style.Definition().Name
	prompt := strings.Join([]string{
		"codex-link-clawbot 回复呈现",
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

func (h *Handler) setResponseMode(userID string, mode presentation.ResponseMode) string {
	if h.preferences == nil || !mode.Valid() {
		return "codex-link-clawbot 回复方式当前不可用。"
	}
	if mode == presentation.ResponseReading && h.visual == nil {
		return "阅读模式需要启用视觉卡片。"
	}
	if mode == presentation.ResponseVoice && (h.visual == nil || h.voice == nil) {
		return "语音模式需要同时启用视觉卡片和语音提供商。"
	}
	if err := h.preferences.SetResponseMode(userID, mode); err != nil {
		log.Printf("[preference] failed to persist response mode for %s: %v", ilink.LogLabel(userID), err)
		return "codex-link-clawbot 回复方式保存失败，请稍后重试。"
	}
	definition := mode.Definition()
	options := []controlOption{
		{Label: "选择其他 codex-link-clawbot 回复方式", Action: actionResponseModes},
		{Label: "返回主菜单", Action: actionMain},
	}
	prompt := strings.Join([]string{
		"codex-link-clawbot 回复方式已切换",
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
	definitions := presentation.Styles()
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

func (h *Handler) setVisualStyle(userID string, style presentation.Style) string {
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
	return h.openCodexGlobalOverview(ctx, userID)
}

func (h *Handler) currentSessionDetail(ctx context.Context, userID string) string {
	threadAgent, err := h.sessionContext()
	if err != nil {
		return err.Error()
	}
	current, err := h.sessions.Current(ctx, userID, threadAgent)
	if err != nil {
		if errors.Is(err, thread.ErrNoActive) {
			options := []controlOption{
				{Label: "新建线程", Action: actionPromptNewSession},
				{Label: "从全局目录选择", Action: actionCodexGlobalThreadPage, Page: 1},
			}
			prompt := "当前没有目标线程。发送普通内容会在当前工作空间新建，也可以从 Codex 全局目录接管。\n\n" + renderControlOptions(options)
			if !h.storeChoice(userID, viewSessionCurrent, options, actionSessionMenu) {
				return controlStateFailureResult().Text
			}
			return prompt + "\n\n回复数字即可，0 返回。"
		}
		return formatSessionError(err)
	}
	options := []controlOption{
		{Label: "线程关系图", Action: actionThreadRelations},
		{Label: "重命名线程", Action: actionPromptRenameSession},
		{Label: "分叉线程", Action: actionForkThread},
		{Label: pinThreadLabel(current.Info.IsPinned), Action: actionToggleThreadPin, Value: fmt.Sprintf("%t", !current.Info.IsPinned)},
		{Label: "压缩上下文", Action: actionCompactThread},
		{Label: "设置线程目标", Action: actionPromptThreadGoal},
		{Label: "清除线程目标", Action: actionClearThreadGoal},
		{Label: "模型与推理强度", Action: actionThreadModels},
		{Label: "审查未提交改动", Action: actionReviewThread},
		{Label: "切换其他线程", Action: actionCodexGlobalThreadPage, Page: 1},
		{Label: "归档线程", Action: actionConfirmArchive},
		{Label: "永久删除线程", Action: actionConfirmDeleteThread},
	}
	prompt := formatSessionDetail("当前线程", current) + h.currentThreadSettingsSummary(userID) + h.currentGoalSummary(ctx, userID) + "\n\n" + renderControlOptions(options)
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
	var page thread.Page
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
		items := make([]thread.ManagedThread, 0, len(matches))
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
			return fmt.Sprintf("没有找到包含“%s”的%s。发送“线程列表”查看全部候选。", query, sessionKind(archived))
		}
		if archived {
			return "没有已归档线程。"
		}
		return "还没有可切换的线程。发送“新建线程”即可创建。"
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
		projectName := workbenchField(item.Workspace.Name)
		if projectName == "" {
			projectName = "未识别"
		}
		label := strings.Join([]string{
			threadTitle(item.Info), "项目 " + projectName, "目录 " + workbenchDirectoryField(item.Info.Cwd), formatThreadStatus(item.Info.Status),
		}, " · ")
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
	title := "选择线程"
	if !autoUse {
		title = "线程列表"
	}
	if archived {
		title = "恢复线程"
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
	prompt := "搜索线程\n\n发送名称、短编号或记得的连续字符，回复 0 返回。"
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
		options = append(options, controlOption{Label: "恢复这个线程", Action: actionRestoreSession, Value: detail.Info.ID})
	} else if detail.Current {
		options = append(options,
			controlOption{Label: "重命名线程", Action: actionPromptRenameSession},
			controlOption{Label: pinThreadLabel(detail.Info.IsPinned), Action: actionToggleThreadPin, Value: fmt.Sprintf("%t", !detail.Info.IsPinned)},
			controlOption{Label: "分叉线程", Action: actionForkThread},
			controlOption{
				Label: "归档这个线程", Action: actionConfirmArchiveItem, Value: detail.Info.ID,
				Page: source.Page, Query: source.Query,
			},
		)
	} else {
		options = append(options,
			controlOption{Label: "切换到这个线程", Action: actionUseSession, Value: detail.Info.ID},
			controlOption{
				Label: "归档这个线程", Action: actionConfirmArchiveItem, Value: detail.Info.ID,
				Page: source.Page, Query: source.Query,
			},
		)
	}
	options = append(options, controlOption{
		Label: "返回线程列表", Action: actionSessionPage, Page: source.Page,
		Archived: source.Archived, Query: source.Query, AutoUse: false,
	})
	title := "线程详情"
	if source.Archived {
		title = "归档线程详情"
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
	options := []controlOption{{Label: "确认归档这个线程", Action: actionArchiveItem, Value: detail.Info.ID}}
	prompt := "准备归档线程：" + threadTitle(detail.Info) + "\n\n归档后可从“恢复已归档线程”找回。\n\n" + renderControlOptions(options)
	if !h.storeChoiceWithBack(userID, viewSessionArchive, options, back) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复 1 确认，0 返回线程详情。"
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
	} else if !errors.Is(currentErr, thread.ErrNoActive) {
		currentName = "暂不可读"
	}
	options := []controlOption{
		{Label: "线程列表", Action: actionBrowseSessions},
		{Label: "恢复已归档线程", Action: actionPickArchivedSession},
	}
	prompt := "线程已归档。\n当前：" + currentName + "\n\n" + renderControlOptions(options)
	if !h.storeChoice(userID, viewSessionResult, options, actionSessionMenu) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字继续，0 返回 Codex 线程。"
}

func paginateManagedThreads(items []thread.ManagedThread, pageNumber, pageSize int) (thread.Page, error) {
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
		return thread.Page{}, fmt.Errorf("page %d exceeds total pages %d", pageNumber, totalPages)
	}
	start := (pageNumber - 1) * pageSize
	end := start + pageSize
	if end > len(items) {
		end = len(items)
	}
	return thread.Page{
		Items: items[start:end], Number: pageNumber,
		TotalPages: totalPages, Total: len(items),
	}, nil
}

func (h *Handler) promptNewSessionName(userID string) string {
	prompt := "新建线程\n\n发送线程名称；回复 0 或“跳过”可创建未命名线程。"
	if !h.storeInput(userID, viewSessionNewInput, controlNewSessionName, actionSessionMenu) {
		return controlStateFailureResult().Text
	}
	return prompt
}

func (h *Handler) promptRenameSession(userID string) string {
	prompt := "重命名线程\n\n发送新的线程名称，回复 0 返回。"
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
		thread, err := h.sessions.New(ctx, userID, threadAgent, strings.TrimSpace(name))
		if err != nil {
			return formatSessionError(err)
		}
		return h.sessionSuccess(userID, "已创建并切换到新线程。", thread)
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
		return h.sessionSuccess(userID, "已切换线程。", thread)
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
		return h.sessionSuccess(userID, "线程已重命名。", thread)
	})
}

func (h *Handler) withRuntimeMutation(action func() string) string {
	return h.withRuntimeMutationPage(func() controlPage { return textControlPage(action()) }).Text
}

func (h *Handler) withRuntimeMutationPage(action func() controlPage) controlPage {
	if h.coordinator == nil {
		return action()
	}
	result := controlPage{}
	if !h.coordinator.TryRuntimeControl(func() { result = action() }) {
		return textControlPage("Codex 正在执行另一个轮次，线程修改暂不可用。Codex 工作空间切换和新请求排队不受影响。")
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
	prompt := "准备归档线程：" + threadTitle(current.Info) + "\n\n" + renderControlOptions(options)
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
			{Label: "新建线程", Action: actionPromptNewSession},
			{Label: "恢复已归档线程", Action: actionPickArchivedSession},
		}
		prompt := "线程已归档。\n当前：未创建\n\n下一条普通消息会自动创建新线程。\n\n" + renderControlOptions(options)
		if !h.storeChoice(userID, viewSessionResult, options, actionSessionMenu) {
			return controlStateFailureResult().Text
		}
		return prompt + "\n\n回复数字继续，0 返回 Codex 线程。"
	}
	currentName := thread.ShortCode(nextActive)
	if current, currentErr := h.sessions.Current(ctx, userID, threadAgent); currentErr == nil {
		currentName = threadTitle(current.Info)
	}
	options := []controlOption{
		{Label: "查看当前线程", Action: actionCurrentSession},
		{Label: "恢复已归档线程", Action: actionPickArchivedSession},
		{Label: "Codex 线程", Action: actionSessionMenu},
	}
	prompt := "线程已归档。\n当前：" + currentName + "\n\n" + renderControlOptions(options)
	if !h.storeChoice(userID, viewSessionResult, options, actionSessionMenu) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字继续，0 返回 Codex 线程。"
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
		options = append(options, controlOption{Label: "查看当前线程", Action: actionCurrentSession})
	} else {
		options = append(options,
			controlOption{Label: "切换到已恢复线程", Action: actionUseSession, Value: thread.ID},
			controlOption{Label: "查看当前线程", Action: actionCurrentSession},
		)
	}
	options = append(options, controlOption{Label: "Codex 线程", Action: actionSessionMenu})
	prompt := "线程已恢复。\n" + h.threadIdentity(userID, thread) + "\n\n" + renderControlOptions(options)
	if !h.storeChoice(userID, viewSessionResult, options, actionSessionMenu) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字继续，0 返回 Codex 线程。"
}

func (h *Handler) sessionSuccess(userID, headline string, thread codex.ThreadInfo) string {
	options := []controlOption{
		{Label: "查看当前线程", Action: actionCurrentSession},
		{Label: "线程列表", Action: actionBrowseSessions},
		{Label: "Codex 线程", Action: actionSessionMenu},
	}
	prompt := headline + "\n" + h.threadIdentity(userID, thread) + "\n\n" + renderControlOptions(options)
	if !h.storeChoice(userID, viewSessionResult, options, actionSessionMenu) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字继续，或直接发送内容开始对话；0 返回 Codex 线程。"
}

func (h *Handler) storeChoice(userID string, view controlView, options []controlOption, back controlAction) bool {
	return h.storeChoiceWithBack(userID, view, options, controlOption{Action: back})
}

// storeChoiceWithBack 为分页详情保留完整返回位置，避免移动端反复翻页。
func (h *Handler) storeChoiceWithBack(userID string, view controlView, options []controlOption, back controlOption) bool {
	return h.storeChoiceWithTTL(userID, view, options, back, controlStateTTL)
}

// storeChoiceWithTTL 只允许总目录延长有效期；具体对象列表和输入仍保持十分钟边界。
func (h *Handler) storeChoiceWithTTL(userID string, view controlView, options []controlOption, back controlOption, ttl time.Duration) bool {
	if h.controlStates == nil {
		log.Printf("[control] persistent state store is unavailable for %s", ilink.LogLabel(userID))
		return false
	}
	state := controlState{
		View: view,
		Mode: controlChoice, Options: append([]controlOption(nil), options...),
		Back: back, ExpiresAt: time.Now().Add(ttl),
	}
	if _, err := h.controlStates.Put(userID, state); err != nil {
		logControlStateError(userID, err)
		return false
	}
	return true
}

func (h *Handler) storeInput(userID string, view controlView, mode controlMode, back controlAction) bool {
	return h.storeInputWithBack(userID, view, mode, controlOption{Action: back})
}

// storeInputWithBack 为待输入表单冻结项目和工作流身份，避免输入期间切换项目后误改其他定义。
func (h *Handler) storeInputWithBack(userID string, view controlView, mode controlMode, back controlOption) bool {
	if h.controlStates == nil {
		log.Printf("[control] persistent state store is unavailable for %s", ilink.LogLabel(userID))
		return false
	}
	state := controlState{
		View: view, Mode: mode, Back: back, ExpiresAt: time.Now().Add(controlStateTTL),
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
	log.Printf("[control] persistent state failed category=%s for %s: %v", statefile.ErrorCategory(err), ilink.LogLabel(userID), err)
}

func controlStateFailureResult() ActionResult {
	return newActionResult("system.control_unavailable", control.DomainSystem, "操作状态暂不可用。请稍后重新发送“菜单”。")
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
		lines = append(lines, fmt.Sprintf("%s  %s", resolvedControlCode(option, index), option.Label))
	}
	return strings.Join(lines, "\n")
}

func resolvedControlCode(option controlOption, index int) string {
	if code := strings.TrimSpace(option.Code); code != "" {
		return code
	}
	return strconv.Itoa(index + 1)
}

func controlOptionByCode(code string, options []controlOption) (controlOption, bool) {
	code = strings.TrimSpace(code)
	for index, option := range options {
		if resolvedControlCode(option, index) == code {
			return option, true
		}
	}
	return controlOption{}, false
}

func controlNavigationOption(text string, options []controlOption) (controlOption, bool) {
	forward := isOneOf(text, "下一页", "下页", "往后", "后面")
	backward := isOneOf(text, "上一页", "上页", "往前", "前面")
	if !forward && !backward {
		return controlOption{}, false
	}
	for _, option := range options {
		if option.Action != actionSessionPage && option.Action != actionCodexGlobalThreadPage && option.Action != actionActivityPage {
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
		"发送“菜单”打开操作总览，回复图片中的数字编号或“下一页”“上一页”完成选择。",
		"微信控制不接收斜杠命令；新建、状态、模型、目标、审查、技能和工具都通过数字编号进入。",
		"偏好与安全只修改个人回复偏好；目录、命令和密钥必须在本机配置。",
		"发送“视觉风格”可在五套完整模板间切换，选择会自动保存。",
		"也可以直接说“切换项目”“新建线程”“搜索线程”“切换线程 登录”或“运行中心”。",
		"codex-link-clawbot 请求开始执行后，发送“状态”查看投递进度，发送“取消”可中止当前执行。",
		"仅当 Codex 轮次正在运行时，发送“追加指令 …”才会调整该轮次方向。",
	}, "\n")
}

func mutationBusyText() string {
	return "当前 Codex 轮次仍在运行，暂时不能切换项目或修改线程。发送“状态”查看 codex-link-clawbot 执行进度，发送“追加指令 …”可调整轮次方向，或发送“取消”中止。"
}

func sessionKind(archived bool) string {
	if archived {
		return "已归档线程"
	}
	return "线程"
}

func pinThreadLabel(pinned bool) string {
	if pinned {
		return "取消置顶线程"
	}
	return "置顶线程"
}

func (h *Handler) currentThreadSettingsSummary(userID string) string {
	if h.sessions == nil {
		return ""
	}
	settings, err := h.sessions.CurrentSettings(userID)
	if err != nil {
		return ""
	}
	model := settings.Model
	if model == "" {
		model = "Codex 默认模型"
	}
	return "\n模型：" + model + "\n推理强度：" + displayEffort(settings.Effort)
}

func (h *Handler) currentGoalSummary(ctx context.Context, userID string) string {
	if h.sessions == nil || h.codex == nil {
		return ""
	}
	advanced, ok := h.codex.(codex.AdvancedThreadClient)
	if !ok {
		return ""
	}
	goal, exists, err := h.sessions.CurrentGoal(ctx, userID, advanced)
	if err != nil || !exists {
		return "\n线程目标：未设置"
	}
	return fmt.Sprintf("\n线程目标：%s\n目标状态：%s\n目标用量：%d 个令牌", normalizeSessionLine(goal.Objective, 160), displayGoalStatus(goal.Status), goal.TokensUsed)
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

func matchSessions(items []thread.ManagedThread, query string) []sessionMatch {
	queryKey := fuzzyKey(query)
	if queryKey == "" {
		return nil
	}
	matches := make([]sessionMatch, 0, len(items))
	for _, item := range items {
		score := bestFuzzyScore(queryKey,
			fuzzyKey(threadSearchTitle(item.Info)),
			fuzzyKey(thread.ShortCode(item.Info.ID)),
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

func formatSessionDetail(title string, managed thread.ManagedThread) string {
	projectName := workbenchField(managed.Workspace.Name)
	if projectName == "" {
		projectName = "未识别"
	}
	lines := []string{
		title,
		"名称：" + threadTitle(managed.Info),
		"项目：" + projectName,
		"短编号：" + thread.ShortCode(managed.Info.ID),
		"状态：" + formatThreadStatus(managed.Info.Status),
	}
	position := "可用"
	if managed.Archived {
		position = "已归档"
	} else if managed.Current {
		position = "当前"
	}
	lines = append(lines, "位置："+position)
	if managed.Info.IsPinned {
		lines = append(lines, "置顶：是")
	}
	if managed.Info.SessionID != "" {
		lines = append(lines, "线程树根："+thread.ShortCode(managed.Info.SessionID))
	}
	if managed.Info.ForkedFromID != "" {
		lines = append(lines, "分叉自："+thread.ShortCode(managed.Info.ForkedFromID))
	}
	if managed.Info.GitInfo != nil && managed.Info.GitInfo.Branch != "" {
		lines = append(lines, "Git 分支："+managed.Info.GitInfo.Branch)
	}
	if len(managed.Info.InstructionSources) > 0 {
		lines = append(lines, fmt.Sprintf("指令来源：%d 个", len(managed.Info.InstructionSources)))
	}
	if managed.Info.Cwd != "" {
		lines = append(lines, "文件目录："+managed.Info.Cwd)
	}
	if preview := sanitizeThreadPreview(managed.Info.Preview); preview != "未命名线程" && preview != threadTitle(managed.Info) {
		lines = append(lines, "摘要："+normalizeSessionLine(preview, 96))
	}
	if managed.Info.CreatedAt > 0 {
		lines = append(lines, "创建："+formatSessionTime(managed.Info.CreatedAt))
	}
	if managed.Info.UpdatedAt > 0 {
		lines = append(lines, "更新："+formatSessionTime(managed.Info.UpdatedAt))
	}
	return strings.Join(lines, "\n")
}

func formatThreadIdentity(info codex.ThreadInfo) string {
	lines := []string{
		"名称：" + threadTitle(info),
		"短编号：" + thread.ShortCode(info.ID),
		"状态：" + formatThreadStatus(info.Status),
	}
	if strings.TrimSpace(info.Cwd) != "" {
		lines = append(lines, "文件目录："+info.Cwd)
	}
	return strings.Join(lines, "\n")
}

func (h *Handler) threadIdentity(userID string, info codex.ThreadInfo) string {
	identity := formatThreadIdentity(info)
	if h.projects == nil {
		return identity
	}
	return "项目：" + workbenchField(h.projects.Current(userID).Name) + "\n" + identity
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
	return "未命名线程"
}

func sanitizeThreadPreview(preview string) string {
	preview = strings.ReplaceAll(preview, "\r\n", "\n")
	for _, marker := range []string{"[codex-link-clawbot 入站文件]", "[codex-link-clawbot 交付物回传]"} {
		if index := strings.Index(preview, marker); index >= 0 {
			preview = preview[:index]
		}
	}
	preview = strings.TrimSpace(preview)
	if index := strings.IndexByte(preview, '\n'); index >= 0 {
		preview = strings.TrimSpace(preview[:index])
	}
	if preview == "" {
		return "未命名线程"
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
		return "未知"
	}
}

func formatSessionTime(timestamp int64) string {
	return time.Unix(timestamp, 0).Local().Format("2006-01-02 15:04")
}

func formatSessionError(err error) string {
	switch {
	case errors.Is(err, thread.ErrNoActive):
		return "当前工作空间没有目标线程。发送普通内容会自动创建，或从全局目录接管。"
	case errors.Is(err, thread.ErrNotOwned):
		return "本地焦点索引中没有这个线程；请从 Codex 全局目录重新接管。"
	case errors.Is(err, thread.ErrAmbiguousCode):
		return "线程编号不唯一，请输入更完整的名称或编号。"
	default:
		return fmt.Sprintf("线程操作失败：%v", err)
	}
}
