package messaging

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/huixiangyang/weclaw/ilink"
)

const (
	maxVoiceTextRunes = 2500
	maxVoiceBytes     = 20 << 20
)

// VoiceProvider 是所有语音提供商必须实现的最小契约，输出统一为 MP3。
type VoiceProvider interface {
	ID() string
	Generate(context.Context, string) ([]byte, error)
}

// VoiceProviderEntry 按配置顺序组成回退链，每个提供商拥有独立超时。
type VoiceProviderEntry struct {
	Provider VoiceProvider
	Timeout  time.Duration
}

type VoiceSynthesis struct {
	ProviderID string
	Audio      []byte
}

type VoiceBriefing struct {
	providers []VoiceProviderEntry
}

func NewVoiceBriefing(providers []VoiceProviderEntry) *VoiceBriefing {
	return &VoiceBriefing{providers: append([]VoiceProviderEntry(nil), providers...)}
}

func (v *VoiceBriefing) Generate(ctx context.Context, text string) (VoiceSynthesis, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return VoiceSynthesis{}, fmt.Errorf("语音文本不能为空")
	}
	if len([]rune(text)) > maxVoiceTextRunes {
		return VoiceSynthesis{}, fmt.Errorf("语音文本不能超过 %d 字", maxVoiceTextRunes)
	}
	if len(v.providers) == 0 {
		return VoiceSynthesis{}, fmt.Errorf("没有可用的语音提供商")
	}

	failures := make([]string, 0, len(v.providers))
	for _, entry := range v.providers {
		if entry.Provider == nil {
			failures = append(failures, "未知提供商：未初始化")
			continue
		}
		providerCtx, cancel := context.WithTimeout(ctx, entry.Timeout)
		audio, err := entry.Provider.Generate(providerCtx, text)
		cancel()
		if err == nil && len(audio) > 0 && len(audio) <= maxVoiceBytes && isMP3(audio) {
			return VoiceSynthesis{ProviderID: entry.Provider.ID(), Audio: audio}, nil
		}
		if ctx.Err() != nil {
			return VoiceSynthesis{}, ctx.Err()
		}
		if err == nil {
			err = fmt.Errorf("返回了无效的 MP3 音频")
		}
		failures = append(failures, fmt.Sprintf("%s：%s", entry.Provider.ID(), normalizeSessionLine(err.Error(), 160)))
	}
	return VoiceSynthesis{}, fmt.Errorf("所有语音提供商均失败：%s", strings.Join(failures, "；"))
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
		return "语音简报未启用。需要配置语音提供商。"
	}
	h.controlVoice.Store(userID, true)
	return "正在生成语音简报。"
}

func (h *Handler) sendVoiceBriefing(ctx context.Context, client *ilink.Client, userID, contextToken string) (string, error) {
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
	synthesis, err := h.voice.Generate(ctx, strings.Join(parts, ""))
	if err != nil {
		return "", err
	}
	if err := SendVoice(ctx, client, userID, synthesis.Audio, contextToken); err != nil {
		return "", err
	}
	return synthesis.ProviderID, nil
}
