package messaging

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const maxMiMoResponseBytes = 29 << 20

type voiceHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type MiMoVoiceProviderConfig struct {
	BaseURL     string
	APIKey      string
	Model       string
	Voice       string
	StylePrompt string
}

type MiMoVoiceProvider struct {
	id     string
	config MiMoVoiceProviderConfig
	client voiceHTTPClient
}

func NewMiMoVoiceProvider(id string, config MiMoVoiceProviderConfig) *MiMoVoiceProvider {
	return newMiMoVoiceProviderWithClient(id, config, &http.Client{})
}

func newMiMoVoiceProviderWithClient(id string, config MiMoVoiceProviderConfig, client voiceHTTPClient) *MiMoVoiceProvider {
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	config.APIKey = strings.TrimSpace(config.APIKey)
	config.Model = strings.TrimSpace(config.Model)
	config.Voice = strings.TrimSpace(config.Voice)
	config.StylePrompt = strings.TrimSpace(config.StylePrompt)
	return &MiMoVoiceProvider{id: id, config: config, client: client}
}

func (v *MiMoVoiceProvider) ID() string { return v.id }

type mimoTTSMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type mimoTTSRequest struct {
	Model    string           `json:"model"`
	Messages []mimoTTSMessage `json:"messages"`
	Audio    struct {
		Format string `json:"format"`
		Voice  string `json:"voice"`
	} `json:"audio"`
}

type mimoTTSResponse struct {
	Choices []struct {
		Message struct {
			Audio *struct {
				Data   string `json:"data"`
				Format string `json:"format"`
			} `json:"audio"`
		} `json:"message"`
	} `json:"choices"`
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
	Message string `json:"message"`
}

func (v *MiMoVoiceProvider) Generate(ctx context.Context, text string) (VoiceAudio, error) {
	payload := mimoTTSRequest{Model: v.config.Model}
	if v.config.StylePrompt != "" {
		payload.Messages = append(payload.Messages, mimoTTSMessage{Role: "user", Content: v.config.StylePrompt})
	}
	// MiMo 会合成 assistant 消息中的文字，user 消息仅用于控制播报风格。
	payload.Messages = append(payload.Messages, mimoTTSMessage{Role: "assistant", Content: text})
	payload.Audio.Format = "mp3"
	payload.Audio.Voice = v.config.Voice
	body, err := json.Marshal(payload)
	if err != nil {
		return VoiceAudio{}, fmt.Errorf("编码 MiMo TTS 请求: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, v.config.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return VoiceAudio{}, fmt.Errorf("创建 MiMo TTS 请求: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+v.config.APIKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := v.client.Do(request)
	if err != nil {
		return VoiceAudio{}, fmt.Errorf("MiMo TTS 请求失败: %s", v.redactSecret(err.Error()))
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxMiMoResponseBytes+1))
	if err != nil {
		return VoiceAudio{}, fmt.Errorf("读取 MiMo TTS 响应: %w", err)
	}
	if len(responseBody) > maxMiMoResponseBytes {
		return VoiceAudio{}, fmt.Errorf("MiMo TTS 响应超过大小限制")
	}

	var completion mimoTTSResponse
	if err := json.Unmarshal(responseBody, &completion); err != nil {
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return VoiceAudio{}, fmt.Errorf("MiMo TTS 返回 HTTP %d", response.StatusCode)
		}
		return VoiceAudio{}, fmt.Errorf("解析 MiMo TTS 响应: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(completion.Error.Message)
		if message == "" {
			message = strings.TrimSpace(completion.Message)
		}
		if message == "" {
			message = http.StatusText(response.StatusCode)
		}
		return VoiceAudio{}, fmt.Errorf("MiMo TTS 返回 HTTP %d: %s", response.StatusCode, normalizeSessionLine(v.redactSecret(message), 160))
	}
	if len(completion.Choices) == 0 || completion.Choices[0].Message.Audio == nil {
		return VoiceAudio{}, fmt.Errorf("MiMo TTS 未返回音频")
	}
	audioPayload := completion.Choices[0].Message.Audio
	if audioPayload.Format != "" && !strings.EqualFold(audioPayload.Format, "mp3") {
		return VoiceAudio{}, fmt.Errorf("MiMo TTS 返回了非 MP3 音频")
	}
	if base64.StdEncoding.DecodedLen(len(audioPayload.Data)) > maxVoiceBytes {
		return VoiceAudio{}, fmt.Errorf("MiMo TTS 音频超过大小限制")
	}
	audio, err := base64.StdEncoding.DecodeString(audioPayload.Data)
	if err != nil {
		return VoiceAudio{}, fmt.Errorf("解码 MiMo TTS 音频: %w", err)
	}
	if len(audio) == 0 || len(audio) > maxVoiceBytes || !isMP3(audio) {
		return VoiceAudio{}, fmt.Errorf("MiMo TTS 返回了无效的 MP3 音频")
	}
	return VoiceAudio{Data: audio, Format: VoiceAudioMP3}, nil
}

func (v *MiMoVoiceProvider) redactSecret(message string) string {
	if v.config.APIKey == "" {
		return message
	}
	return strings.ReplaceAll(message, v.config.APIKey, "[redacted]")
}
