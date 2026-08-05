package messaging

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os/exec"
	"strings"
	"time"

	"github.com/huixiangyang/weclaw/ilink"
)

const (
	maxVoiceTextRunes     = 2500
	maxVoiceBytes         = 20 << 20
	maxVoicePCMBytes      = 16000 * 2 * 300
	wechatVoiceSampleRate = 16000
	wechatVoiceFrameBytes = wechatVoiceSampleRate * 2 * 20 / 1000
)

type VoiceAudioFormat string

const (
	VoiceAudioMP3 VoiceAudioFormat = "mp3"
	VoiceAudioWAV VoiceAudioFormat = "wav"
)

// VoiceAudio 保留提供商的原始格式，微信发送层统一完成 PCM/SILK 编码。
type VoiceAudio struct {
	Data   []byte
	Format VoiceAudioFormat
}

// VoiceProvider 是所有语音提供商必须实现的最小契约。
type VoiceProvider interface {
	ID() string
	Generate(context.Context, string) (VoiceAudio, error)
}

// VoiceProviderEntry 按配置顺序组成回退链，每个提供商拥有独立超时。
type VoiceProviderEntry struct {
	Provider VoiceProvider
	Timeout  time.Duration
}

type VoiceSynthesis struct {
	ProviderID string
	Audio      VoiceAudio
}

type VoiceBriefing struct {
	ffmpegCommand string
	silkCommand   string
	providers     []VoiceProviderEntry
}

func NewVoiceBriefing(ffmpegCommand, silkCommand string, providers []VoiceProviderEntry) *VoiceBriefing {
	return &VoiceBriefing{ffmpegCommand: ffmpegCommand, silkCommand: silkCommand, providers: append([]VoiceProviderEntry(nil), providers...)}
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
		if err == nil {
			err = validateVoiceAudio(audio)
		}
		if err == nil {
			return VoiceSynthesis{ProviderID: entry.Provider.ID(), Audio: audio}, nil
		}
		if ctx.Err() != nil {
			return VoiceSynthesis{}, ctx.Err()
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

func isWAV(data []byte) bool {
	return len(data) >= 12 && bytes.Equal(data[:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WAVE"))
}

func validateVoiceAudio(audio VoiceAudio) error {
	if len(audio.Data) == 0 || len(audio.Data) > maxVoiceBytes {
		return fmt.Errorf("音频大小必须在 1 字节到 20 MiB 之间")
	}
	switch audio.Format {
	case VoiceAudioMP3:
		if !isMP3(audio.Data) {
			return fmt.Errorf("返回了无效的 MP3 音频")
		}
	case VoiceAudioWAV:
		if !isWAV(audio.Data) {
			return fmt.Errorf("返回了无效的 WAV 音频")
		}
	default:
		return fmt.Errorf("不支持音频格式 %q", audio.Format)
	}
	return nil
}

type WeChatVoice struct {
	Data       []byte
	PlaytimeMS int
}

// EncodeWeChatVoice 将任意提供商音频收敛为微信原生语音条要求的 16 kHz SILK V3。
func EncodeWeChatVoice(ctx context.Context, ffmpegCommand, silkCommand string, audio VoiceAudio) (WeChatVoice, error) {
	if err := validateVoiceAudio(audio); err != nil {
		return WeChatVoice{}, err
	}
	process := exec.CommandContext(ctx, ffmpegCommand,
		"-hide_banner", "-loglevel", "error", "-i", "pipe:0",
		"-vn", "-sn", "-dn", "-f", "s16le", "-acodec", "pcm_s16le",
		"-ar", fmt.Sprintf("%d", wechatVoiceSampleRate), "-ac", "1", "pipe:1",
	)
	process.Stdin = bytes.NewReader(audio.Data)
	stdout := &boundedVoiceOutputBuffer{remaining: maxVoicePCMBytes + wechatVoiceFrameBytes}
	stderr := &boundedVoiceBuffer{remaining: maxVoiceProcessLogBytes}
	process.Stdout = stdout
	process.Stderr = stderr
	if err := process.Run(); err != nil {
		if ctx.Err() != nil {
			return WeChatVoice{}, ctx.Err()
		}
		message := normalizeSessionLine(stderr.String(), 200)
		if message == "" {
			message = err.Error()
		}
		return WeChatVoice{}, fmt.Errorf("FFmpeg 语音转码失败: %s", message)
	}
	if stdout.exceeded || stdout.buffer.Len() > maxVoicePCMBytes {
		return WeChatVoice{}, fmt.Errorf("微信语音超过 5 分钟限制")
	}
	pcm := stdout.buffer.Bytes()
	pcm = pcm[:len(pcm)-len(pcm)%wechatVoiceFrameBytes]
	if len(pcm) == 0 {
		return WeChatVoice{}, fmt.Errorf("FFmpeg 未生成完整语音帧")
	}
	silkData, err := runSILKEncoder(ctx, silkCommand, pcm)
	if err != nil {
		return WeChatVoice{}, err
	}
	if len(silkData) <= 10 || len(silkData) > maxVoiceBytes || !bytes.Equal(silkData[:10], []byte("\x02#!SILK_V3")) {
		return WeChatVoice{}, fmt.Errorf("SILK 编码器返回了无效音频")
	}
	return WeChatVoice{Data: silkData, PlaytimeMS: len(pcm) * 1000 / (wechatVoiceSampleRate * 2)}, nil
}

func runSILKEncoder(ctx context.Context, command string, pcm []byte) ([]byte, error) {
	process := exec.CommandContext(ctx, command)
	process.Stdin = bytes.NewReader(pcm)
	stdout := &boundedVoiceOutputBuffer{remaining: maxVoiceBytes + 1}
	stderr := &boundedVoiceBuffer{remaining: maxVoiceProcessLogBytes}
	process.Stdout = stdout
	process.Stderr = stderr
	if err := process.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		message := normalizeSessionLine(stderr.String(), 200)
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("SILK 编码失败: %s", message)
	}
	if stdout.exceeded || stdout.buffer.Len() > maxVoiceBytes {
		return nil, fmt.Errorf("SILK 音频超过 20 MiB 限制")
	}
	return append([]byte(nil), stdout.buffer.Bytes()...), nil
}

type boundedVoiceOutputBuffer struct {
	buffer    bytes.Buffer
	remaining int
	exceeded  bool
}

func (b *boundedVoiceOutputBuffer) Write(data []byte) (int, error) {
	originalLength := len(data)
	if len(data) > b.remaining {
		data = data[:b.remaining]
		b.exceeded = true
	}
	if len(data) > 0 {
		_, _ = b.buffer.Write(data)
		b.remaining -= len(data)
	}
	return originalLength, nil
}

func SendVoice(ctx context.Context, client *ilink.Client, userID string, voice WeChatVoice, contextToken string) error {
	if len(voice.Data) == 0 || len(voice.Data) > maxVoiceBytes || !bytes.HasPrefix(voice.Data, []byte("\x02#!SILK_V3")) || voice.PlaytimeMS <= 0 {
		return fmt.Errorf("voice data must be valid Tencent SILK with a positive playtime")
	}
	if strings.TrimSpace(contextToken) == "" {
		return fmt.Errorf("发送微信语音必须使用当前会话 context token")
	}
	uploaded, err := UploadFileToCDN(ctx, client, voice.Data, userID, ilink.CDNMediaTypeVoice)
	if err != nil {
		return fmt.Errorf("upload voice: %w", err)
	}
	log.Printf("[voice] uploaded native SILK for %s (bytes=%d playtime_ms=%d)", ilink.LogLabel(userID), len(voice.Data), voice.PlaytimeMS)
	item := newVoiceMessageItem(uploaded, voice.PlaytimeMS)
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
	log.Printf("[voice] WeChat accepted native voice for %s", ilink.LogLabel(userID))
	return nil
}

func newVoiceMessageItem(uploaded *UploadedFile, playtimeMS int) ilink.MessageItem {
	return ilink.MessageItem{Type: ilink.ItemTypeVoice, VoiceItem: &ilink.VoiceItem{
		Media:         &ilink.MediaInfo{EncryptQueryParam: uploaded.DownloadParam, AESKey: AESKeyToBase64(uploaded.AESKeyHex), EncryptType: 1},
		EncodeType:    6,
		BitsPerSample: 16,
		SampleRate:    wechatVoiceSampleRate,
		Playtime:      playtimeMS,
	}}
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
	log.Printf("[voice] synthesized provider=%s format=%s bytes=%d for %s", synthesis.ProviderID, synthesis.Audio.Format, len(synthesis.Audio.Data), ilink.LogLabel(userID))
	encoded, err := EncodeWeChatVoice(ctx, h.voice.ffmpegCommand, h.voice.silkCommand, synthesis.Audio)
	if err != nil {
		return "", err
	}
	log.Printf("[voice] encoded Tencent SILK bytes=%d playtime_ms=%d for %s", len(encoded.Data), encoded.PlaytimeMS, ilink.LogLabel(userID))
	if err := SendVoice(ctx, client, userID, encoded, contextToken); err != nil {
		return "", err
	}
	return synthesis.ProviderID, nil
}
