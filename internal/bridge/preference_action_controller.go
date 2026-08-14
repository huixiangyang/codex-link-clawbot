package bridge

import (
	"github.com/huixiangyang/codex-link-clawbot/internal/control"
	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
)

func (h *Handler) executePreferenceControlAction(userID string, option controlOption) ActionResult {
	var text string
	switch option.Action {
	case actionVisualStyles:
		text = h.openVisualStyles(userID)
	case actionSetVisualStyle:
		text = h.setVisualStyle(userID, presentation.Style(option.Value))
	case actionResponseModes:
		text = h.openResponseModes(userID)
	case actionSetResponseMode:
		text = h.setResponseMode(userID, presentation.ResponseMode(option.Value))
	default:
		return invalidControlAction(option.Action, control.DomainPreference)
	}
	return controlTextResult(option.Action, control.DomainPreference, text)
}

func (h *Handler) dispatchPreferenceIntent(userID string, resolved control.Resolved, argument string) ActionResult {
	var text string
	switch resolved.Definition.ID {
	case control.IntentResponseModes:
		text = h.openResponseModes(userID)
	case control.IntentResponseVoice:
		text = h.setResponseMode(userID, presentation.ResponseVoice)
	case control.IntentResponseAdaptive:
		text = h.setResponseMode(userID, presentation.ResponseAdaptive)
	case control.IntentResponseReading:
		text = h.setResponseMode(userID, presentation.ResponseReading)
	case control.IntentVisualStyle:
		if argument == "" {
			text = h.openVisualStyles(userID)
		} else if style, ok := presentation.ResolveStyle(argument); ok {
			text = h.setVisualStyle(userID, style)
		} else {
			text = "没有这个视觉风格。发送“视觉风格”查看可选模板。"
		}
	default:
		return invalidIntentResult(resolved)
	}
	return intentTextResult(resolved, text)
}
