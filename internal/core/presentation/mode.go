package presentation

type ResponseMode string

const (
	ResponseText     ResponseMode = "text"
	ResponseAdaptive ResponseMode = "adaptive"
	ResponseReading  ResponseMode = "reading"
	ResponseVoice    ResponseMode = "voice"
)

type ResponseModeDefinition struct {
	ID          ResponseMode `json:"id"`
	Name        string       `json:"name"`
	Description string       `json:"description"`
}

var responseModeDefinitions = []ResponseModeDefinition{
	{ID: ResponseText, Name: "完整文字", Description: "直接发送全文，文件按需取回"},
	{ID: ResponseAdaptive, Name: "简洁通知", Description: "短答直接读，长答与文件按数字取回"},
	{ID: ResponseReading, Name: "阅读", Description: "所有回答优先阅读卡"},
	{ID: ResponseVoice, Name: "语音", Description: "MP3 朗读，长回答读节选，全文按需取回"},
}

func ResponseModes() []ResponseModeDefinition {
	return append([]ResponseModeDefinition(nil), responseModeDefinitions...)
}

func (mode ResponseMode) Valid() bool {
	switch mode {
	case ResponseText, ResponseAdaptive, ResponseReading, ResponseVoice:
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
