package voice

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/statefile"
)

const maxVoiceProcessLogBytes = 16 << 10

type PiperConfig struct {
	TemporaryRoot string
	Command       string
	Model         string
	ModelConfig   string
	LengthScale   float64
}

type PiperProvider struct {
	id     string
	config PiperConfig
}

func NewPiper(id string, config PiperConfig) *PiperProvider {
	return &PiperProvider{id: id, config: config}
}

func (v *PiperProvider) ID() string { return v.id }

func (v *PiperProvider) Generate(ctx context.Context, text string) (Audio, error) {
	root := v.config.TemporaryRoot
	if root == "" {
		return Audio{}, fmt.Errorf("Piper temporary root is required")
	}
	if err := statefile.EnsurePrivateDirectory(root); err != nil {
		return Audio{}, err
	}
	dir, err := os.MkdirTemp(root, "piper-*")
	if err != nil {
		return Audio{}, fmt.Errorf("创建 Piper 临时目录: %w", err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o700); err != nil {
		return Audio{}, fmt.Errorf("保护 Piper 临时目录: %w", err)
	}
	wavPath := filepath.Join(dir, "voice.wav")

	piperArgs := []string{"-m", v.config.Model, "-c", v.config.ModelConfig, "--length-scale", strconv.FormatFloat(v.config.LengthScale, 'f', -1, 64), "-f", wavPath, "--", text}
	if err := runVoiceCommand(ctx, v.config.Command, piperArgs...); err != nil {
		return Audio{}, fmt.Errorf("Piper 合成失败: %w", err)
	}
	if err := validateVoiceFile(wavPath, 50<<20); err != nil {
		return Audio{}, fmt.Errorf("Piper 未生成有效 WAV: %w", err)
	}
	if err := os.Chmod(wavPath, 0o600); err != nil {
		return Audio{}, fmt.Errorf("保护 Piper WAV: %w", err)
	}
	audio, err := readVoiceFile(wavPath, 50<<20)
	if err != nil {
		return Audio{}, fmt.Errorf("读取 Piper WAV: %w", err)
	}
	if !isWAV(audio) {
		return Audio{}, fmt.Errorf("Piper 返回了无效的 WAV 音频")
	}
	return Audio{Data: audio, Format: WAV}, nil
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
		message := presentation.NormalizeLine(stderr.String(), 200)
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

func readVoiceFile(path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	audio, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(audio) == 0 || int64(len(audio)) > maxBytes {
		return nil, fmt.Errorf("音频文件大小无效")
	}
	return audio, nil
}
