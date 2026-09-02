package bridge

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	maxVoiceTextRunes = 2500
	maxVoiceBytes     = 20 << 20
)

type VoiceAudioFormat string

const (
	VoiceAudioMP3 VoiceAudioFormat = "mp3"
	VoiceAudioWAV VoiceAudioFormat = "wav"
)

// VoiceAudio 保留提供商的原始格式，微信发送层统一压缩为 MP3 文件。
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
	providers     []VoiceProviderEntry
}

func NewVoiceBriefing(ffmpegCommand string, providers []VoiceProviderEntry) *VoiceBriefing {
	return &VoiceBriefing{ffmpegCommand: ffmpegCommand, providers: append([]VoiceProviderEntry(nil), providers...)}
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

// EncodeVoiceMP3 将提供商音频统一压缩为适合微信移动端下载播放的单声道 MP3。
func EncodeVoiceMP3(ctx context.Context, ffmpegCommand string, audio VoiceAudio) ([]byte, error) {
	if err := validateVoiceAudio(audio); err != nil {
		return nil, err
	}
	process := exec.CommandContext(ctx, ffmpegCommand,
		"-hide_banner", "-loglevel", "error", "-i", "pipe:0",
		"-vn", "-sn", "-dn", "-f", "mp3", "-codec:a", "libmp3lame",
		"-b:a", "64k", "-ar", "24000", "-ac", "1", "pipe:1",
	)
	process.Stdin = bytes.NewReader(audio.Data)
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
		return nil, fmt.Errorf("FFmpeg MP3 转码失败: %s", message)
	}
	if stdout.exceeded || stdout.buffer.Len() > maxVoiceBytes {
		return nil, fmt.Errorf("MP3 音频超过 20 MiB 限制")
	}
	mp3 := stdout.buffer.Bytes()
	if !isMP3(mp3) {
		return nil, fmt.Errorf("FFmpeg 未生成有效 MP3")
	}
	return append([]byte(nil), mp3...), nil
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
