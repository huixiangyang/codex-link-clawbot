package presentation

type ResponseMode string

const (
	ResponseAdaptive ResponseMode = "adaptive"
	ResponseReading  ResponseMode = "reading"
	ResponseVoice    ResponseMode = "voice"
)

type ResponseModeDefinition struct {
	ID          ResponseMode
	Name        string
	Description string
}

var responseModeDefinitions = []ResponseModeDefinition{
	{ID: ResponseAdaptive, Name: "自适应", Description: "短答文字，长答阅读卡"},
	{ID: ResponseReading, Name: "阅读", Description: "所有回答优先阅读卡"},
	{ID: ResponseVoice, Name: "语音", Description: "阅读卡与 MP3 配套交付"},
}

func ResponseModes() []ResponseModeDefinition {
	return append([]ResponseModeDefinition(nil), responseModeDefinitions...)
}

func (mode ResponseMode) Valid() bool {
	switch mode {
	case ResponseAdaptive, ResponseReading, ResponseVoice:
		return true
	default:
		return false
	}
}

func (mode ResponseMode) Definition() ResponseModeDefinition {
	for _, definition := range responseModeDefinitions {
		if definition.ID == mode {
			return definition
		}
	}
	return responseModeDefinitions[0]
}
