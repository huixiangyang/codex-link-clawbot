package messaging

import (
	"context"
	"strings"
)

func (h *Handler) dispatchIntent(ctx context.Context, userID string, resolved ResolvedIntent) ActionResult {
	definition := resolved.Definition
	argument := cleanIntentArgument(resolved.Argument)
	if h.intentMutationBlocked(userID, resolved, argument) {
		return intentTextResult(resolved, mutationBusyText())
	}
	switch definition.Domain {
	case DomainTask:
		return h.dispatchTaskIntent(userID, resolved)
	case DomainProject:
		return h.dispatchProjectIntent(userID, resolved, argument)
	case DomainSession:
		return h.dispatchSessionIntent(ctx, userID, resolved, argument)
	case DomainPreference:
		return h.dispatchPreferenceIntent(userID, resolved, argument)
	case DomainLibrary:
		return h.dispatchLibraryIntent(userID, resolved)
	case DomainAutomation:
		return h.dispatchAutomationIntent(userID, resolved)
	case DomainSecurity:
		return h.dispatchSecurityIntent(userID, resolved)
	default:
		return h.dispatchSystemIntent(ctx, userID, resolved)
	}
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
