package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
)

const CurrentSchemaVersion = 5

// Config 明确分隔 Codex 本身与 codex-link-clawbot 接入层配置。
type Config struct {
	SchemaVersion int           `json:"schema_version"`
	Codex         CodexConfig   `json:"codex"`
	Clawbot       ClawbotConfig `json:"codex-link-clawbot"`
}

// UnmarshalJSON 要求磁盘配置显式声明版本，同时保留调用方预置的默认值。
func (c *Config) UnmarshalJSON(data []byte) error {
	type configWire Config
	decoded := configWire(*c)
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("trailing data")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if _, exists := fields["schema_version"]; !exists {
		return fmt.Errorf("schema_version is required")
	}
	*c = Config(decoded)
	return nil
}

// ClawbotConfig 只描述微信接入层、机器能力和确定性功能，不承载 Codex 线程偏好。
type ClawbotConfig struct {
	ProjectEntries []ProjectConfig `json:"project_entries"`
	Reply          ReplyConfig     `json:"reply"`
	Security       SecurityConfig  `json:"security"`
}

// ReplyConfig 统一管理从等待提示到最终媒体交付的微信回复体验。
type ReplyConfig struct {
	Progress ProgressConfig `json:"progress"`
	Visual   VisualConfig   `json:"visual"`
	Voice    VoiceConfig    `json:"voice"`
}

type SecurityConfig struct {
	RemoteLockCode string `json:"remote_lock_code,omitempty"`
}

func (c SecurityConfig) validate() error {
	if c.RemoteLockCode == "" {
		return nil
	}
	length := len([]rune(c.RemoteLockCode))
	if length < 6 || length > 64 || strings.ContainsAny(c.RemoteLockCode, "\r\n") {
		return fmt.Errorf("codex-link-clawbot.security.remote_lock_code must be a single line with 6 to 64 characters")
	}
	return nil
}

type VoiceConfig struct {
	Enabled       bool                  `json:"enabled"`
	FFmpegCommand string                `json:"ffmpeg_command,omitempty"`
	Providers     []VoiceProviderConfig `json:"providers,omitempty"`
}

type VoiceProviderConfig struct {
	ID             string                    `json:"id"`
	Type           string                    `json:"type"`
	TimeoutSeconds int                       `json:"timeout_seconds"`
	Piper          *PiperVoiceProviderConfig `json:"piper,omitempty"`
	MiMo           *MiMoVoiceProviderConfig  `json:"mimo,omitempty"`
}

type PiperVoiceProviderConfig struct {
	Command     string  `json:"command"`
	Model       string  `json:"model"`
	ModelConfig string  `json:"model_config"`
	LengthScale float64 `json:"length_scale"`
}

type MiMoVoiceProviderConfig struct {
	BaseURL     string `json:"base_url"`
	APIKey      string `json:"api_key,omitempty"`
	Model       string `json:"model"`
	Voice       string `json:"voice"`
	StylePrompt string `json:"style_prompt,omitempty"`
}

func (c VoiceConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if !filepath.IsAbs(c.FFmpegCommand) || filepath.Clean(c.FFmpegCommand) != c.FFmpegCommand {
		return fmt.Errorf("codex-link-clawbot.reply.voice.ffmpeg_command must be a clean absolute path")
	}
	if len(c.Providers) == 0 || len(c.Providers) > 4 {
		return fmt.Errorf("codex-link-clawbot.reply.voice.providers must contain between 1 and 4 providers")
	}
	ids := make(map[string]struct{}, len(c.Providers))
	for index, provider := range c.Providers {
		prefix := fmt.Sprintf("codex-link-clawbot.reply.voice.providers[%d]", index)
		if !projectIDPattern.MatchString(provider.ID) {
			return fmt.Errorf("%s.id is invalid", prefix)
		}
		if _, exists := ids[provider.ID]; exists {
			return fmt.Errorf("codex-link-clawbot.reply.voice provider id %q is duplicated", provider.ID)
		}
		ids[provider.ID] = struct{}{}
		if provider.TimeoutSeconds < 5 || provider.TimeoutSeconds > 180 {
			return fmt.Errorf("%s.timeout_seconds must be between 5 and 180", prefix)
		}
		switch provider.Type {
		case "piper":
			if provider.Piper == nil || provider.MiMo != nil {
				return fmt.Errorf("%s must contain only piper configuration", prefix)
			}
			if err := provider.Piper.validate(prefix + ".piper"); err != nil {
				return err
			}
		case "mimo":
			if provider.MiMo == nil || provider.Piper != nil {
				return fmt.Errorf("%s must contain only mimo configuration", prefix)
			}
			if err := provider.MiMo.validate(prefix + ".mimo"); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%s.type must be piper or mimo", prefix)
		}
	}
	return nil
}

func (c PiperVoiceProviderConfig) validate(prefix string) error {
	paths := []struct {
		field string
		path  string
	}{
		{field: "command", path: c.Command},
		{field: "model", path: c.Model},
		{field: "model_config", path: c.ModelConfig},
	}
	for _, item := range paths {
		if !filepath.IsAbs(item.path) || filepath.Clean(item.path) != item.path {
			return fmt.Errorf("%s.%s must be a clean absolute path", prefix, item.field)
		}
	}
	if c.LengthScale < 0.5 || c.LengthScale > 2 {
		return fmt.Errorf("%s.length_scale must be between 0.5 and 2", prefix)
	}
	return nil
}

func (c MiMoVoiceProviderConfig) validate(prefix string) error {
	baseURL, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil || baseURL.Host == "" || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return fmt.Errorf("%s.base_url must be an absolute MiMo API URL without credentials, query, or fragment", prefix)
	}
	if baseURL.Scheme != "https" && !(baseURL.Scheme == "http" && isLoopbackHost(baseURL.Hostname())) {
		return fmt.Errorf("%s.base_url must use HTTPS except for a loopback test endpoint", prefix)
	}
	if strings.TrimSpace(c.APIKey) == "" || strings.ContainsAny(c.APIKey, "\r\n") {
		return fmt.Errorf("%s.api_key is required and must be a single line", prefix)
	}
	if c.Model != "mimo-v2.5-tts" {
		return fmt.Errorf("%s.model must be mimo-v2.5-tts", prefix)
	}
	switch c.Voice {
	case "冰糖", "茉莉", "苏打", "白桦", "Mia", "Chloe", "Milo", "Dean":
	default:
		return fmt.Errorf("%s.voice is not a supported MiMo preset voice", prefix)
	}
	if len([]rune(c.StylePrompt)) > 500 {
		return fmt.Errorf("%s.style_prompt must contain at most 500 characters", prefix)
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// CodexConfig 是唯一智能体运行配置。App Server 参数由程序固定，避免协议分叉。
type CodexConfig struct {
	Command string            `json:"command"`
	Env     map[string]string `json:"env,omitempty"`
	Model   string            `json:"model,omitempty"`
}

func defaultCodexConfig() CodexConfig {
	return CodexConfig{Command: "codex"}
}

func (c CodexConfig) validate() error {
	if strings.TrimSpace(c.Command) == "" {
		return fmt.Errorf("codex.command is required")
	}
	return nil
}

// ProjectConfig 是远程工作空间的安全边界，Codex 只能在这里声明的目录间切换。
type ProjectConfig struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Root string `json:"root"`
}

var projectIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

func defaultProjectRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "codex-link-clawbot-workspace")
	}
	return filepath.Join(home, ".codex-link-clawbot", "workspace")
}

func defaultProjects() []ProjectConfig {
	return []ProjectConfig{{ID: "workspace", Name: "默认项目", Root: defaultProjectRoot()}}
}

func validateProjects(projects []ProjectConfig) error {
	if len(projects) == 0 {
		return fmt.Errorf("codex-link-clawbot.project_entries must contain at least one project entry")
	}
	ids := make(map[string]struct{}, len(projects))
	names := make(map[string]struct{}, len(projects))
	for index, project := range projects {
		prefix := fmt.Sprintf("codex-link-clawbot.project_entries[%d]", index)
		if !projectIDPattern.MatchString(project.ID) {
			return fmt.Errorf("%s.id is invalid", prefix)
		}
		if _, exists := ids[project.ID]; exists {
			return fmt.Errorf("project id %q is duplicated", project.ID)
		}
		ids[project.ID] = struct{}{}
		name := strings.TrimSpace(project.Name)
		if name == "" {
			return fmt.Errorf("%s.name is required", prefix)
		}
		if strings.ContainsAny(name, "\r\n") || len([]rune(name)) > 40 {
			return fmt.Errorf("%s.name must be a single line with at most 40 characters", prefix)
		}
		if _, exists := names[name]; exists {
			return fmt.Errorf("project name %q is duplicated", name)
		}
		names[name] = struct{}{}
		if !filepath.IsAbs(project.Root) || filepath.Clean(project.Root) != project.Root {
			return fmt.Errorf("%s.root must be a clean absolute path", prefix)
		}
	}
	return nil
}

// VisualConfig 控制微信操作卡片的 HTML 到 PNG 渲染。
// 浏览器为空时自动发现 Playwright 管理的 Chromium 或系统 Chrome。
type VisualConfig struct {
	Enabled           bool   `json:"enabled"`
	BrowserCommand    string `json:"browser_command,omitempty"`
	LongReplies       bool   `json:"long_replies"`
	LongReplyMinRunes int    `json:"long_reply_min_runes"`
}

func defaultVisualConfig() VisualConfig {
	return VisualConfig{Enabled: true, LongReplies: true, LongReplyMinRunes: 900}
}

func (c VisualConfig) validate() error {
	if c.BrowserCommand != "" && !filepath.IsAbs(c.BrowserCommand) {
		return fmt.Errorf("codex-link-clawbot.reply.visual.browser_command must be an absolute path")
	}
	if c.Enabled && c.LongReplies && (c.LongReplyMinRunes < 300 || c.LongReplyMinRunes > 5000) {
		return fmt.Errorf("codex-link-clawbot.reply.visual.long_reply_min_runes must be between 300 and 5000")
	}
	return nil
}

// ProgressConfig 控制长任务在微信端的进度提示和保活节奏。
type ProgressConfig struct {
	Enabled                  bool `json:"enabled"`
	TypingIntervalSeconds    int  `json:"typing_interval_seconds"`
	FirstMessageDelaySeconds int  `json:"first_message_delay_seconds"`
	MessageIntervalSeconds   int  `json:"message_interval_seconds"`
}

func defaultProgressConfig() ProgressConfig {
	return ProgressConfig{
		Enabled:                  true,
		TypingIntervalSeconds:    8,
		FirstMessageDelaySeconds: 15,
		MessageIntervalSeconds:   45,
	}
}

func (c ProgressConfig) validate() error {
	if !c.Enabled {
		return nil
	}
	if c.TypingIntervalSeconds < 3 || c.TypingIntervalSeconds > 30 {
		return fmt.Errorf("codex-link-clawbot.reply.progress.typing_interval_seconds must be between 3 and 30")
	}
	if c.FirstMessageDelaySeconds < 5 || c.FirstMessageDelaySeconds > 120 {
		return fmt.Errorf("codex-link-clawbot.reply.progress.first_message_delay_seconds must be between 5 and 120")
	}
	if c.MessageIntervalSeconds < 15 || c.MessageIntervalSeconds > 300 {
		return fmt.Errorf("codex-link-clawbot.reply.progress.message_interval_seconds must be between 15 and 300")
	}
	return nil
}

// DefaultConfig returns an empty configuration.
func DefaultConfig() *Config {
	return &Config{
		SchemaVersion: CurrentSchemaVersion,
		Codex:         defaultCodexConfig(),
		Clawbot: ClawbotConfig{
			ProjectEntries: defaultProjects(),
			Reply: ReplyConfig{
				Progress: defaultProgressConfig(),
				Visual:   defaultVisualConfig(),
			},
		},
	}
}

// ConfigPath returns the path to the config file.
func ConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex-link-clawbot", "config.json"), nil
}

// Load loads configuration from disk and environment variables.
func Load() (*Config, error) {
	cfg := DefaultConfig()

	path, err := ConfigPath()
	if err != nil {
		return cfg, nil
	}

	found, err := statefile.ReadJSON(path, cfg, statefile.Options{MaxBytes: 4 << 20})
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if !found {
		loadEnv(cfg)
		return cfg, cfg.validate()
	}
	loadEnv(cfg)
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

// decodeConfig 只负责严格解析本地配置，不读取环境变量，便于独立验证不可信输入。
func decodeConfig(data []byte) (*Config, error) {
	cfg := DefaultConfig()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("parse config: trailing data")
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if c.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("schema_version must be %d", CurrentSchemaVersion)
	}
	if err := c.Codex.validate(); err != nil {
		return err
	}
	if err := validateProjects(c.Clawbot.ProjectEntries); err != nil {
		return err
	}
	if err := c.Clawbot.Reply.Progress.validate(); err != nil {
		return err
	}
	if err := c.Clawbot.Reply.Visual.validate(); err != nil {
		return err
	}
	if c.Clawbot.Reply.Voice.Enabled && !c.Clawbot.Reply.Visual.Enabled {
		return fmt.Errorf("codex-link-clawbot.reply.voice.enabled requires codex-link-clawbot.reply.visual.enabled for paired image and audio delivery")
	}
	if err := c.Clawbot.Reply.Voice.validate(); err != nil {
		return err
	}
	if err := c.Clawbot.Security.validate(); err != nil {
		return err
	}
	return nil
}

func loadEnv(cfg *Config) {
	if v := os.Getenv("CODEX_LINK_CLAWBOT_CODEX_COMMAND"); v != "" {
		cfg.Codex.Command = v
	}
	if v := os.Getenv("CODEX_LINK_CLAWBOT_CODEX_MODEL"); v != "" {
		cfg.Codex.Model = v
	}
	if v := os.Getenv("CODEX_LINK_CLAWBOT_VISUAL_BROWSER"); v != "" {
		cfg.Clawbot.Reply.Visual.BrowserCommand = v
	}
	if v := os.Getenv("CODEX_LINK_CLAWBOT_MIMO_API_KEY"); v != "" {
		for index := range cfg.Clawbot.Reply.Voice.Providers {
			if cfg.Clawbot.Reply.Voice.Providers[index].Type == "mimo" && cfg.Clawbot.Reply.Voice.Providers[index].MiMo != nil {
				cfg.Clawbot.Reply.Voice.Providers[index].MiMo.APIKey = v
			}
		}
	}
}

// Save saves the configuration to disk.
func Save(cfg *Config) error {
	if cfg == nil {
		return fmt.Errorf("config is required")
	}
	if err := cfg.validate(); err != nil {
		return err
	}
	path, err := ConfigPath()
	if err != nil {
		return err
	}

	return statefile.WriteJSON(path, cfg, statefile.Options{
		MaxBytes: 4 << 20,
		Validate: cfg.validate,
	})
}
