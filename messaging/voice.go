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
	"time"

	"github.com/huixiangyang/weclaw/ilink"
)

const (
	maxVoiceTextRunes    = 2500
	maxVoiceBytes        = 20 << 20
	maxMiMoResponseBytes = 29 << 20
)

type voiceHTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// VoiceBriefingConfig 是 MiMo TTS 的最小运行契约；输出格式固定为微信可发送的 MP3。
type VoiceBriefingConfig struct {
	BaseURL     string
	APIKey      string
	Model       string
	Voice       string
	StylePrompt string
}

// VoiceBriefing 直接调用 MiMo Chat Completions TTS，不执行本机脚本。
type VoiceBriefing struct {
	config VoiceBriefingConfig
	client voiceHTTPClient
}

func NewVoiceBriefing(config VoiceBriefingConfig) *VoiceBriefing {
	return newVoiceBriefingWithClient(config, &http.Client{Timeout: 90 * time.Second})
}

func newVoiceBriefingWithClient(config VoiceBriefingConfig, client voiceHTTPClient) *VoiceBriefing {
	config.BaseURL = strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	config.APIKey = strings.TrimSpace(config.APIKey)
	config.Model = strings.TrimSpace(config.Model)
	config.Voice = strings.TrimSpace(config.Voice)
	config.StylePrompt = strings.TrimSpace(config.StylePrompt)
	return &VoiceBriefing{config: config, client: client}
}

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

func (v *VoiceBriefing) Generate(ctx context.Context, text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, fmt.Errorf("MiMo TTS 文本不能为空")
	}
	if len([]rune(text)) > maxVoiceTextRunes {
		return nil, fmt.Errorf("MiMo TTS 文本不能超过 %d 字", maxVoiceTextRunes)
	}

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
		return nil, fmt.Errorf("编码 MiMo TTS 请求: %w", err)
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, v.config.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("创建 MiMo TTS 请求: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+v.config.APIKey)
	request.Header.Set("Content-Type", "application/json")

	response, err := v.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("MiMo TTS 请求失败: %s", v.redactSecret(err.Error()))
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxMiMoResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取 MiMo TTS 响应: %w", err)
	}
	if len(responseBody) > maxMiMoResponseBytes {
		return nil, fmt.Errorf("MiMo TTS 响应超过大小限制")
	}

	var completion mimoTTSResponse
	if err := json.Unmarshal(responseBody, &completion); err != nil {
		if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
			return nil, fmt.Errorf("MiMo TTS 返回 HTTP %d", response.StatusCode)
		}
		return nil, fmt.Errorf("解析 MiMo TTS 响应: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(completion.Error.Message)
		if message == "" {
			message = strings.TrimSpace(completion.Message)
		}
		if message == "" {
			message = http.StatusText(response.StatusCode)
		}
		return nil, fmt.Errorf("MiMo TTS 返回 HTTP %d: %s", response.StatusCode, normalizeSessionLine(v.redactSecret(message), 160))
	}
	if len(completion.Choices) == 0 || completion.Choices[0].Message.Audio == nil {
		return nil, fmt.Errorf("MiMo TTS 未返回音频")
	}
	audioPayload := completion.Choices[0].Message.Audio
	if audioPayload.Format != "" && !strings.EqualFold(audioPayload.Format, "mp3") {
		return nil, fmt.Errorf("MiMo TTS 返回了非 MP3 音频")
	}
	if base64.StdEncoding.DecodedLen(len(audioPayload.Data)) > maxVoiceBytes {
		return nil, fmt.Errorf("MiMo TTS 音频超过大小限制")
	}
	audio, err := base64.StdEncoding.DecodeString(audioPayload.Data)
	if err != nil {
		return nil, fmt.Errorf("解码 MiMo TTS 音频: %w", err)
	}
	if len(audio) == 0 || len(audio) > maxVoiceBytes || !isMP3(audio) {
		return nil, fmt.Errorf("MiMo TTS 返回了无效的 MP3 音频")
	}
	return audio, nil
}

func (v *VoiceBriefing) redactSecret(message string) string {
	if v.config.APIKey == "" {
		return message
	}
	return strings.ReplaceAll(message, v.config.APIKey, "[redacted]")
}

func isMP3(data []byte) bool {
	if len(data) >= 3 && bytes.Equal(data[:3], []byte("ID3")) {
		return true
	}
	return len(data) >= 2 && data[0] == 0xff && data[1]&0xe0 == 0xe0
}

func SendVoice(ctx context.Context, client *ilink.Client, userID string, data []byte, contextToken string) error {
	if len(data) == 0 || len(data) > maxVoiceBytes || !isMP3(data) {
		return fmt.Errorf("voice data must be a valid MP3 up to 20 MiB")
	}
	uploaded, err := UploadFileToCDN(ctx, client, data, userID, ilink.CDNMediaTypeFile)
	if err != nil {
		return fmt.Errorf("upload voice: %w", err)
	}
	item := ilink.MessageItem{Type: ilink.ItemTypeVoice, VoiceItem: &ilink.VoiceItem{
		Media:     &ilink.MediaInfo{EncryptQueryParam: uploaded.DownloadParam, AESKey: AESKeyToBase64(uploaded.AESKeyHex), EncryptType: 1},
		VoiceSize: uploaded.CipherSize, EncodeType: 7,
	}}
	response, err := client.SendMessage(ctx, &ilink.SendMessageRequest{
		Msg: ilink.SendMsg{
			FromUserID: client.BotID(), ToUserID: userID, ClientID: NewClientID(),
			MessageType: ilink.MessageTypeBot, MessageState: ilink.MessageStateFinish,
			ItemList: []ilink.MessageItem{item}, ContextToken: contextToken,
		},
	})
	if err != nil {
		return err
	}
	if response.Ret != 0 {
		return fmt.Errorf("send voice failed: ret=%d errmsg=%s", response.Ret, response.ErrMsg)
	}
	return nil
}

func (h *Handler) requestVoiceBriefing(userID string) string {
	if h.voice == nil {
		return "语音简报未启用。需要配置 MiMo TTS。"
	}
	h.controlVoice.Store(userID, true)
	return "语音简报已生成并发送。"
}

func (h *Handler) sendVoiceBriefing(ctx context.Context, client *ilink.Client, userID, contextToken string) error {
	projectName := "未配置项目"
	if h.projects != nil {
		projectName = h.projects.Current(userID).Name
	}
	parts := []string{"WeClaw 工作简报。当前项目：" + projectName + "。"}
	if h.activities == nil || len(h.activities.List(userID)) == 0 {
		parts = append(parts, "目前还没有任务记录。")
	} else {
		records := h.activities.List(userID)
		if len(records) > 3 {
			records = records[:3]
		}
		parts = append(parts, fmt.Sprintf("最近有 %d 项任务。", len(records)))
		for index, record := range records {
			parts = append(parts, fmt.Sprintf("第 %d 项，%s，状态%s。", index+1, record.Summary, formatActivityStatus(record.Status)))
		}
	}
	audio, err := h.voice.Generate(ctx, strings.Join(parts, ""))
	if err != nil {
		return err
	}
	return SendVoice(ctx, client, userID, audio, contextToken)
}
