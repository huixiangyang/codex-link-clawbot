package messaging

import (
	"github.com/huixiangyang/codex-link-clawbot/internal/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
)

func (h *Handler) executePreferenceControlAction(userID string, option controlOption) ActionResult {
	var text string
	switch option.Action {
	case actionVisualStyles:
		text = h.openVisualStyles(userID)
	case actionSetVisualStyle:
		text = h.setVisualStyle(userID, visual.Style(option.Value))
	case actionResponseModes:
		text = h.openResponseModes(userID)
	case actionSetResponseMode:
		text = h.setResponseMode(userID, preference.ResponseMode(option.Value))
	default:
		return invalidControlAction(option.Action, DomainPreference)
	}
	return controlTextResult(option.Action, DomainPreference, text)
}

func (h *Handler) dispatchPreferenceIntent(userID string, resolved ResolvedIntent, argument string) ActionResult {
	var text string
	switch resolved.Definition.ID {
	case IntentResponseModes:
		text = h.openResponseModes(userID)
	case IntentResponseVoice:
		text = h.setResponseMode(userID, preference.ResponseVoice)
	case IntentResponseAdaptive:
		text = h.setResponseMode(userID, preference.ResponseAdaptive)
	case IntentResponseReading:
		text = h.setResponseMode(userID, preference.ResponseReading)
	case IntentVisualStyle:
		if argument == "" {
			text = h.openVisualStyles(userID)
		} else if style, ok := visual.ResolveStyle(argument); ok {
			text = h.setVisualStyle(userID, style)
		} else {
			text = "没有这个视觉风格。发送“视觉风格”查看可选模板。"
		}
	default:
		return invalidIntentResult(resolved)
	}
	return intentTextResult(resolved, text)
}
