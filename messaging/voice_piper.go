package messaging

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

const maxVoiceProcessLogBytes = 16 << 10

type PiperVoiceProviderConfig struct {
	Command       string
	Model         string
	ModelConfig   string
	FFmpegCommand string
	LengthScale   float64
}

type PiperVoiceProvider struct {
	id     string
	config PiperVoiceProviderConfig
}

func NewPiperVoiceProvider(id string, config PiperVoiceProviderConfig) *PiperVoiceProvider {
	return &PiperVoiceProvider{id: id, config: config}
}

func (v *PiperVoiceProvider) ID() string { return v.id }

func (v *PiperVoiceProvider) Generate(ctx context.Context, text string) ([]byte, error) {
	dir, err := os.MkdirTemp("", "weclaw-piper-*")
	if err != nil {
		return nil, fmt.Errorf("创建 Piper 临时目录: %w", err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, fmt.Errorf("保护 Piper 临时目录: %w", err)
	}
	wavPath := filepath.Join(dir, "voice.wav")
	mp3Path := filepath.Join(dir, "voice.mp3")

	piperArgs := []string{"-m", v.config.Model, "-c", v.config.ModelConfig, "--length-scale", strconv.FormatFloat(v.config.LengthScale, 'f', -1, 64), "-f", wavPath, "--", text}
	if err := runVoiceCommand(ctx, v.config.Command, piperArgs...); err != nil {
		return nil, fmt.Errorf("Piper 合成失败: %w", err)
	}
	if err := validateVoiceFile(wavPath, 50<<20); err != nil {
		return nil, fmt.Errorf("Piper 未生成有效 WAV: %w", err)
	}
	if err := os.Chmod(wavPath, 0o600); err != nil {
		return nil, fmt.Errorf("保护 Piper WAV: %w", err)
	}

	ffmpegArgs := []string{"-hide_banner", "-loglevel", "error", "-y", "-i", wavPath, "-codec:a", "libmp3lame", "-b:a", "64k", "-ar", "24000", "-ac", "1", mp3Path}
	if err := runVoiceCommand(ctx, v.config.FFmpegCommand, ffmpegArgs...); err != nil {
		return nil, fmt.Errorf("Piper MP3 转码失败: %w", err)
	}
	if err := os.Chmod(mp3Path, 0o600); err != nil {
		return nil, fmt.Errorf("保护 Piper MP3: %w", err)
	}
	audio, err := readVoiceFile(mp3Path)
	if err != nil {
		return nil, err
	}
	return audio, nil
}

func runVoiceCommand(ctx context.Context, command string, args ...string) error {
	process := exec.CommandContext(ctx, command, args...)
	process.Stdout = io.Discard
	stderr := &boundedVoiceBuffer{remaining: maxVoiceProcessLogBytes}
	process.Stderr = stderr
	if err := process.Run(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		message := normalizeSessionLine(stderr.String(), 200)
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("%s", message)
	}
	return nil
}

type boundedVoiceBuffer struct {
	buffer    bytes.Buffer
	remaining int
}

func (b *boundedVoiceBuffer) Write(data []byte) (int, error) {
	originalLength := len(data)
	if len(data) > b.remaining {
		data = data[:b.remaining]
	}
	if len(data) > 0 {
		_, _ = b.buffer.Write(data)
		b.remaining -= len(data)
	}
	return originalLength, nil
}

func (b *boundedVoiceBuffer) String() string { return b.buffer.String() }

func validateVoiceFile(path string, maxBytes int64) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > maxBytes {
		return fmt.Errorf("文件大小或类型无效")
	}
	return nil
}

func readVoiceFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("读取 Piper MP3: %w", err)
	}
	defer file.Close()
	audio, err := io.ReadAll(io.LimitReader(file, maxVoiceBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取 Piper MP3: %w", err)
	}
	if len(audio) == 0 || len(audio) > maxVoiceBytes || !isMP3(audio) {
		return nil, fmt.Errorf("Piper 返回了无效的 MP3 音频")
	}
	return audio, nil
}
