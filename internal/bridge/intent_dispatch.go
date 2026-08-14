package bridge

import (
	"context"
	"github.com/huixiangyang/codex-link-clawbot/internal/control"
	"strings"
)

func (h *Handler) dispatchIntent(ctx context.Context, userID string, resolved control.Resolved, sourceKey string) ActionResult {
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
	case control.DomainQueue:
		result = h.dispatchTaskIntent(ctx, userID, resolved)
	case control.DomainProject:
		result = h.dispatchProjectIntent(ctx, userID, resolved, argument)
	case control.DomainSession:
		result = h.dispatchSessionIntent(ctx, userID, resolved, argument)
	case control.DomainPreference:
		result = h.dispatchPreferenceIntent(userID, resolved, argument)
	case control.DomainDelivery:
		result = h.dispatchDeliveryIntent(userID, resolved)
	case control.DomainSecurity:
		result = h.dispatchSecurityIntent(userID, resolved)
	default:
		result = h.dispatchSystemIntent(ctx, userID, resolved)
	}
	if receiptReserved && (result.Effect.Kind == EffectEnqueuePrompt || result.Effect.Kind == EffectRetryTask) {
		result = result.withReservedReceiptRollback(userID, sourceKey)
	}
	return result
}

func intentRequiresReceipt(resolved control.Resolved, argument string) bool {
	if !resolved.Definition.RequiresReceipt {
		return false
	}
	switch resolved.Definition.ID {
	case control.IntentProjectSelect, control.IntentSessionSelect, control.IntentSessionRestore, control.IntentVisualStyle, control.IntentThreadGoal:
		return strings.TrimSpace(argument) != ""
	default:
		return true
	}
}

func (h *Handler) reserveControlReceipt(userID, sourceKey, actionID string, domain control.Domain) (bool, ActionResult) {
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

func duplicateControlResult(actionID string, domain control.Domain) ActionResult {
	return newActionResult(actionID, domain, "这条操作已经处理，不会重复执行。发送“菜单”查看当前状态。")
}

func intentTextResult(resolved control.Resolved, text string) ActionResult {
	return newActionResult(string(resolved.Definition.ID), resolved.Definition.Domain, text)
}

func invalidIntentResult(resolved control.Resolved) ActionResult {
	return intentTextResult(resolved, "这个操作已经失效。发送“菜单”重新打开操作总览。")
}

func (h *Handler) intentMutationBlocked(userID string, resolved control.Resolved, argument string) bool {
	definition := resolved.Definition
	if !definition.MutatesState || definition.AllowDuringTask || !h.hasActiveTask(userID) {
		return false
	}
	switch definition.ID {
	case control.IntentSessionSelect, control.IntentSessionRestore:
		return strings.TrimSpace(argument) != ""
	default:
		return true
	}
}
