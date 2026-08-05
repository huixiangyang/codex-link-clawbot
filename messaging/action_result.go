package messaging

import (
	"fmt"
	"strings"
)

type ActionDomain string

const (
	DomainSystem     ActionDomain = "system"
	DomainTask       ActionDomain = "task"
	DomainProject    ActionDomain = "project"
	DomainSession    ActionDomain = "session"
	DomainPreference ActionDomain = "preference"
	DomainLibrary    ActionDomain = "library"
	DomainAutomation ActionDomain = "automation"
	DomainSecurity   ActionDomain = "security"
)

func (domain ActionDomain) valid() bool {
	switch domain {
	case DomainSystem, DomainTask, DomainProject, DomainSession, DomainPreference, DomainLibrary, DomainAutomation, DomainSecurity:
		return true
	default:
		return false
	}
}

type ActionEffectKind string

const (
	EffectNone          ActionEffectKind = ""
	EffectEnqueuePrompt ActionEffectKind = "enqueue_prompt"
	EffectRetryTask     ActionEffectKind = "retry_task"
	EffectFrozenText    ActionEffectKind = "frozen_text"
	EffectSendMedia     ActionEffectKind = "send_media"
	EffectVoiceBriefing ActionEffectKind = "voice_briefing"
)

type ActionEffect struct {
	Kind  ActionEffectKind
	Value string
}

// ActionResult 是控制器与微信投递层之间的唯一结果类型。
// Value 只在进程内传递，不能直接进入卡片、日志或持久菜单。
type ActionResult struct {
	ActionID string
	Domain   ActionDomain
	Text     string
	Effect   ActionEffect
	rollback *controlReceiptRollback
}

type controlReceiptRollback struct {
	OwnerID   string
	SourceKey string
	State     controlState
}

func newActionResult(actionID string, domain ActionDomain, text string) ActionResult {
	return ActionResult{ActionID: actionID, Domain: domain, Text: text}
}

func effectActionResult(actionID string, domain ActionDomain, text string, kind ActionEffectKind, value string) ActionResult {
	return ActionResult{
		ActionID: actionID,
		Domain:   domain,
		Text:     text,
		Effect:   ActionEffect{Kind: kind, Value: value},
	}
}

func (result ActionResult) validate() error {
	if strings.TrimSpace(result.ActionID) == "" || !result.Domain.valid() {
		return fmt.Errorf("action result identity is invalid")
	}
	if result.rollback != nil && result.Effect.Kind != EffectEnqueuePrompt && result.Effect.Kind != EffectRetryTask {
		return fmt.Errorf("control receipt rollback is not allowed for this effect")
	}
	switch result.Effect.Kind {
	case EffectNone:
		if strings.TrimSpace(result.Text) == "" {
			return fmt.Errorf("text action result is empty")
		}
	case EffectEnqueuePrompt, EffectRetryTask, EffectFrozenText:
		if strings.TrimSpace(result.Effect.Value) == "" {
			return fmt.Errorf("action effect value is required")
		}
	case EffectSendMedia:
		if strings.TrimSpace(result.Effect.Value) == "" {
			return fmt.Errorf("action effect value is required")
		}
		if strings.TrimSpace(result.Text) == "" {
			return fmt.Errorf("media action result text is empty")
		}
	case EffectVoiceBriefing:
		if result.Effect.Value != "" {
			return fmt.Errorf("voice briefing effect cannot contain a value")
		}
	default:
		return fmt.Errorf("unknown action effect %q", result.Effect.Kind)
	}
	return nil
}

func (result ActionResult) withIdentity(actionID string, domain ActionDomain) ActionResult {
	result.ActionID = actionID
	result.Domain = domain
	return result
}

func (result ActionResult) withControlRollback(ownerID, sourceKey string, state controlState) ActionResult {
	state.Options = append([]controlOption(nil), state.Options...)
	result.rollback = &controlReceiptRollback{OwnerID: ownerID, SourceKey: sourceKey, State: state}
	return result
}
