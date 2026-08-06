package messaging

import (
	"context"
	"strings"
)

func (h *Handler) dispatchIntent(ctx context.Context, userID string, resolved ResolvedIntent, sourceKey string) ActionResult {
	definition := resolved.Definition
	argument := cleanIntentArgument(resolved.Argument)
	if h.intentMutationBlocked(userID, resolved, argument) {
		return intentTextResult(resolved, mutationBusyText())
	}
	receiptReserved := false
	if intentRequiresReceipt(resolved, argument) {
		reserved, result := h.reserveControlReceipt(userID, sourceKey, string(definition.ID), definition.Domain)
		if !reserved {
			return result
		}
		receiptReserved = true
	}
	var result ActionResult
	switch definition.Domain {
	case DomainQueue:
		result = h.dispatchTaskIntent(ctx, userID, resolved)
	case DomainProject:
		result = h.dispatchProjectIntent(ctx, userID, resolved, argument)
	case DomainSession:
		result = h.dispatchSessionIntent(ctx, userID, resolved, argument)
	case DomainPreference:
		result = h.dispatchPreferenceIntent(userID, resolved, argument)
	case DomainLibrary:
		result = h.dispatchLibraryIntent(userID, resolved)
	case DomainAutomation:
		result = h.dispatchAutomationIntent(userID, resolved)
	case DomainSecurity:
		result = h.dispatchSecurityIntent(userID, resolved)
	default:
		result = h.dispatchSystemIntent(ctx, userID, resolved)
	}
	if receiptReserved && (result.Effect.Kind == EffectEnqueuePrompt || result.Effect.Kind == EffectRetryTask) {
		result = result.withReservedReceiptRollback(userID, sourceKey)
	}
	return result
}

func intentRequiresReceipt(resolved ResolvedIntent, argument string) bool {
	if !resolved.Definition.RequiresReceipt {
		return false
	}
	switch resolved.Definition.ID {
	case IntentProjectSelect, IntentSessionSelect, IntentSessionRestore, IntentVisualStyle, IntentThreadGoal:
		return strings.TrimSpace(argument) != ""
	default:
		return true
	}
}

func (h *Handler) reserveControlReceipt(userID, sourceKey, actionID string, domain ActionDomain) (bool, ActionResult) {
	if h.controlStates == nil {
		return false, controlStateFailureResult()
	}
	if strings.TrimSpace(sourceKey) == "" {
		return false, newActionResult(actionID, domain, "这条微信消息没有稳定来源编号，无法安全执行控制操作。")
	}
	reserved, err := h.controlStates.ReserveReceipt(userID, sourceKey, actionID, domain)
	if err != nil {
		logControlStateError(userID, err)
		return false, controlStateFailureResult().withIdentity(actionID, domain)
	}
	if !reserved {
		return false, duplicateControlResult(actionID, domain)
	}
	return true, ActionResult{}
}

func duplicateControlResult(actionID string, domain ActionDomain) ActionResult {
	return newActionResult(actionID, domain, "这条操作已经处理，不会重复执行。发送 / 查看当前状态。")
}

func intentTextResult(resolved ResolvedIntent, text string) ActionResult {
	return newActionResult(string(resolved.Definition.ID), resolved.Definition.Domain, text)
}

func invalidIntentResult(resolved ResolvedIntent) ActionResult {
	return intentTextResult(resolved, "这个操作已经失效。发送 / 重新打开菜单。")
}

func (h *Handler) intentMutationBlocked(userID string, resolved ResolvedIntent, argument string) bool {
	definition := resolved.Definition
	if !definition.MutatesState || definition.AllowDuringTask || !h.hasActiveTask(userID) {
		return false
	}
	switch definition.ID {
	case IntentSessionSelect, IntentSessionRestore:
		return strings.TrimSpace(argument) != ""
	default:
		return true
	}
}
