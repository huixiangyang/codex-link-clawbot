package bridge

import (
	"fmt"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/control"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
)

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
	Kind      ActionEffectKind
	Value     string
	ProjectID string
	ThreadID  string
	NewThread bool
}

// ActionResult 是控制器与微信投递层之间的唯一结果类型。
// Value 只在进程内传递，不能直接进入卡片、日志或持久菜单。
type ActionResult struct {
	ActionID string
	Domain   control.Domain
	Text     string
	Effect   ActionEffect
	visual   *actionVisual
	rollback *controlReceiptRollback
}

// actionVisual 携带控制器已经构造好的专用视图，投递层不得从 Text 反向猜测页面类型。
type actionVisual struct {
	Workbench *visual.Workbench
	Directory *visual.Directory
	ThreadMap *visual.ThreadMap
	Review    *visual.Review
}

type controlPage struct {
	Text   string
	visual *actionVisual
}

type controlReceiptRollback struct {
	OwnerID   string
	SourceKey string
	ActionID  string
	Domain    control.Domain
	State     *controlState
}

func newActionResult(actionID string, domain control.Domain, text string) ActionResult {
	return ActionResult{ActionID: actionID, Domain: domain, Text: text}
}

func pageActionResult(actionID string, domain control.Domain, page controlPage) ActionResult {
	return ActionResult{ActionID: actionID, Domain: domain, Text: page.Text, visual: page.visual}
}

func textControlPage(text string) controlPage {
	return controlPage{Text: text}
}

func effectActionResult(actionID string, domain control.Domain, text string, kind ActionEffectKind, value string) ActionResult {
	return ActionResult{
		ActionID: actionID,
		Domain:   domain,
		Text:     text,
		Effect:   ActionEffect{Kind: kind, Value: value},
	}
}

func (result ActionResult) validate() error {
	if strings.TrimSpace(result.ActionID) == "" || !result.Domain.Valid() {
		return fmt.Errorf("action result identity is invalid")
	}
	if result.rollback != nil && result.Effect.Kind != EffectEnqueuePrompt && result.Effect.Kind != EffectRetryTask {
		return fmt.Errorf("control receipt rollback is not allowed for this effect")
	}
	if result.Effect.ProjectID != "" && (result.Effect.Kind != EffectEnqueuePrompt || !validEffectProjectID(result.Effect.ProjectID)) {
		return fmt.Errorf("action effect project is invalid")
	}
	if result.Effect.ThreadID != "" && (result.Effect.Kind != EffectEnqueuePrompt || result.Effect.NewThread ||
		len(result.Effect.ThreadID) > 512 || strings.TrimSpace(result.Effect.ThreadID) != result.Effect.ThreadID ||
		strings.ContainsAny(result.Effect.ThreadID, "\r\n\x00")) {
		return fmt.Errorf("action effect thread is invalid")
	}
	if result.Effect.NewThread && result.Effect.Kind != EffectEnqueuePrompt {
		return fmt.Errorf("new thread effect is invalid")
	}
	if result.visual != nil && result.visual.count() != 1 {
		return fmt.Errorf("action visual must contain exactly one view")
	}
	if (result.Effect.ThreadID != "" || result.Effect.NewThread) && result.Effect.ProjectID == "" {
		return fmt.Errorf("thread effect requires a frozen project")
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

func (visual *actionVisual) count() int {
	if visual == nil {
		return 0
	}
	count := 0
	if visual.Workbench != nil {
		count++
	}
	if visual.Directory != nil {
		count++
	}
	if visual.ThreadMap != nil {
		count++
	}
	if visual.Review != nil {
		count++
	}
	return count
}

func (result ActionResult) withIdentity(actionID string, domain control.Domain) ActionResult {
	result.ActionID = actionID
	result.Domain = domain
	return result
}

func (result ActionResult) withControlRollback(ownerID, sourceKey string, state controlState) ActionResult {
	state.Options = append([]controlOption(nil), state.Options...)
	result.rollback = &controlReceiptRollback{
		OwnerID: ownerID, SourceKey: sourceKey, ActionID: result.ActionID, Domain: result.Domain, State: &state,
	}
	return result
}

func (result ActionResult) withReservedReceiptRollback(ownerID, sourceKey string) ActionResult {
	result.rollback = &controlReceiptRollback{
		OwnerID: ownerID, SourceKey: sourceKey, ActionID: result.ActionID, Domain: result.Domain,
	}
	return result
}

func (result ActionResult) withProjectID(projectID string) ActionResult {
	result.Effect.ProjectID = strings.TrimSpace(projectID)
	return result
}

func (result ActionResult) withThreadID(threadID string) ActionResult {
	result.Effect.ThreadID = strings.TrimSpace(threadID)
	return result
}

func (result ActionResult) withNewThread() ActionResult {
	result.Effect.NewThread = true
	result.Effect.ThreadID = ""
	return result
}

func validEffectProjectID(value string) bool {
	if len(value) == 0 || len(value) > 32 || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}
