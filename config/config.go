package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Config holds the application configuration.
type Config struct {
	APIAddr          string                  `json:"api_addr,omitempty"`
	SaveDir          string                  `json:"save_dir,omitempty"` // Linkhoard archive directory
	Progress         ProgressConfig          `json:"progress"`
	ScheduledReports []ScheduledReportConfig `json:"scheduled_reports,omitempty"`
	Codex            CodexConfig             `json:"codex"`
	Visual           VisualConfig            `json:"visual"`
}

// CodexConfig 是唯一智能体运行配置。App Server 参数由程序固定，避免协议分叉。
type CodexConfig struct {
	Command string            `json:"command"`
	Cwd     string            `json:"cwd"`
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
	if c.Cwd != "" && !filepath.IsAbs(c.Cwd) {
		return fmt.Errorf("codex.cwd must be an absolute path")
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
		return fmt.Errorf("visual.browser_command must be an absolute path")
	}
	if c.Enabled && c.LongReplies && (c.LongReplyMinRunes < 300 || c.LongReplyMinRunes > 5000) {
		return fmt.Errorf("visual.long_reply_min_runes must be between 300 and 5000")
	}
	return nil
}

// ScheduledReportConfig 定义每日一次的确定性项目巡检。
// 配置项全部显式必填，避免服务以猜测值静默运行。
type ScheduledReportConfig struct {
	Name                string `json:"name"`
	DailyAt             string `json:"daily_at"`
	Timezone            string `json:"timezone"`
	ProjectDir          string `json:"project_dir"`
	ServiceName         string `json:"service_name"`
	HealthURL           string `json:"health_url"`
	CommitLookbackHours int    `json:"commit_lookback_hours"`
}

var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9@_.:-]+$`)

func validateScheduledReports(reports []ScheduledReportConfig) error {
	names := make(map[string]struct{}, len(reports))
	for index, report := range reports {
		prefix := fmt.Sprintf("scheduled_reports[%d]", index)
		name := strings.TrimSpace(report.Name)
		if name == "" {
			return fmt.Errorf("%s.name is required", prefix)
		}
		if _, exists := names[name]; exists {
			return fmt.Errorf("scheduled report name %q is duplicated", name)
		}
		names[name] = struct{}{}
		if _, err := time.Parse("15:04", report.DailyAt); err != nil {
			return fmt.Errorf("%s.daily_at must use HH:MM", prefix)
		}
		if _, err := time.LoadLocation(report.Timezone); err != nil {
			return fmt.Errorf("%s.timezone is invalid: %w", prefix, err)
		}
		if !filepath.IsAbs(report.ProjectDir) {
			return fmt.Errorf("%s.project_dir must be an absolute path", prefix)
		}
		if !serviceNamePattern.MatchString(report.ServiceName) {
			return fmt.Errorf("%s.service_name is invalid", prefix)
		}
		healthURL, err := url.Parse(report.HealthURL)
		if err != nil || (healthURL.Scheme != "http" && healthURL.Scheme != "https") || healthURL.Host == "" {
			return fmt.Errorf("%s.health_url must be an absolute HTTP URL", prefix)
		}
		if report.CommitLookbackHours < 1 || report.CommitLookbackHours > 168 {
			return fmt.Errorf("%s.commit_lookback_hours must be between 1 and 168", prefix)
		}
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
		return fmt.Errorf("progress.typing_interval_seconds must be between 3 and 30")
	}
	if c.FirstMessageDelaySeconds < 5 || c.FirstMessageDelaySeconds > 120 {
		return fmt.Errorf("progress.first_message_delay_seconds must be between 5 and 120")
	}
	if c.MessageIntervalSeconds < 15 || c.MessageIntervalSeconds > 300 {
		return fmt.Errorf("progress.message_interval_seconds must be between 15 and 300")
	}
	return nil
}

// DefaultConfig returns an empty configuration.
func DefaultConfig() *Config {
	return &Config{
		Progress: defaultProgressConfig(),
		Codex:    defaultCodexConfig(),
		Visual:   defaultVisualConfig(),
	}
}

// ConfigPath returns the path to the config file.
func ConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".weclaw", "config.json"), nil
}

// Load loads configuration from disk and environment variables.
func Load() (*Config, error) {
	cfg := DefaultConfig()

	path, err := ConfigPath()
	if err != nil {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			loadEnv(cfg)
			return cfg, cfg.validate()
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("parse config: trailing data")
	}
	loadEnv(cfg)
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) validate() error {
	if err := c.Progress.validate(); err != nil {
		return err
	}
	if err := c.Codex.validate(); err != nil {
		return err
	}
	if err := c.Visual.validate(); err != nil {
		return err
	}
	return validateScheduledReports(c.ScheduledReports)
}

func loadEnv(cfg *Config) {
	if v := os.Getenv("WECLAW_API_ADDR"); v != "" {
		cfg.APIAddr = v
	}
	if v := os.Getenv("WECLAW_SAVE_DIR"); v != "" {
		cfg.SaveDir = v
	}
	if v := os.Getenv("WECLAW_CODEX_COMMAND"); v != "" {
		cfg.Codex.Command = v
	}
	if v := os.Getenv("WECLAW_CODEX_CWD"); v != "" {
		cfg.Codex.Cwd = v
	}
	if v := os.Getenv("WECLAW_CODEX_MODEL"); v != "" {
		cfg.Codex.Model = v
	}
	if v := os.Getenv("WECLAW_VISUAL_BROWSER"); v != "" {
		cfg.Visual.BrowserCommand = v
	}
}

// Save saves the configuration to disk.
func Save(cfg *Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return os.WriteFile(path, data, 0o600)
}
