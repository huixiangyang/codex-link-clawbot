package config

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/huixiangyang/weclaw/internal/statefile"
)

const CurrentSchemaVersion = 2

// Config 明确分隔 Codex 本身与 WeClaw 接入层配置。
type Config struct {
	SchemaVersion int          `json:"schema_version"`
	Codex         CodexConfig  `json:"codex"`
	WeClaw        WeClawConfig `json:"weclaw"`
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

// WeClawConfig 只描述微信接入层、机器能力和确定性功能，不承载 Codex 线程偏好。
type WeClawConfig struct {
	ProjectEntries []ProjectConfig `json:"project_entries"`
	Reply          ReplyConfig     `json:"reply"`
	Features       FeatureConfig   `json:"features"`
	Security       SecurityConfig  `json:"security"`
	SendAPI        SendAPIConfig   `json:"send_api"`
}

// ReplyConfig 统一管理从等待提示到最终媒体交付的微信回复体验。
type ReplyConfig struct {
	Progress ProgressConfig `json:"progress"`
	Visual   VisualConfig   `json:"visual"`
	Voice    VoiceConfig    `json:"voice"`
}

// FeatureConfig 汇总不属于 Codex 的可选 WeClaw 功能。
type FeatureConfig struct {
	LinkArchive LinkArchiveConfig  `json:"link_archive"`
	Automations []AutomationConfig `json:"automations,omitempty"`
}

type LinkArchiveConfig struct {
	Enabled   bool   `json:"enabled"`
	Directory string `json:"directory,omitempty"`
}

func (c LinkArchiveConfig) validate() error {
	if !c.Enabled {
		if strings.TrimSpace(c.Directory) != "" {
			return fmt.Errorf("weclaw.features.link_archive.directory requires enabled")
		}
		return nil
	}
	if !filepath.IsAbs(c.Directory) || filepath.Clean(c.Directory) != c.Directory {
		return fmt.Errorf("weclaw.features.link_archive.directory must be a clean absolute path when enabled")
	}
	return nil
}

type SendAPIConfig struct {
	Enabled           bool                 `json:"enabled"`
	ListenAddr        string               `json:"listen_addr,omitempty"`
	ProxyMode         bool                 `json:"proxy_mode,omitempty"`
	TrustedProxyCIDRs []string             `json:"trusted_proxy_cidrs,omitempty"`
	Tokens            []SendAPITokenConfig `json:"tokens,omitempty"`
}

type SendAPITokenConfig struct {
	CallerID    string   `json:"caller_id"`
	TokenSHA256 string   `json:"token_sha256"`
	Scopes      []string `json:"scopes"`
}

func (c SendAPIConfig) validate() error {
	if c.Enabled && strings.TrimSpace(c.ListenAddr) == "" {
		return fmt.Errorf("weclaw.send_api.listen_addr is required when enabled")
	}
	if c.ListenAddr != "" {
		host, portText, err := net.SplitHostPort(c.ListenAddr)
		if err != nil {
			return fmt.Errorf("weclaw.send_api.listen_addr must use an IP address and explicit port")
		}
		ip := net.ParseIP(host)
		port, portErr := strconv.Atoi(portText)
		if ip == nil || portErr != nil || port < 1 || port > 65535 {
			return fmt.Errorf("weclaw.send_api.listen_addr must use an IP address and explicit port")
		}
		if !c.ProxyMode && !ip.IsLoopback() {
			return fmt.Errorf("weclaw.send_api.listen_addr must be loopback unless proxy_mode is enabled")
		}
	}
	if c.ProxyMode {
		if len(c.TrustedProxyCIDRs) == 0 || len(c.TrustedProxyCIDRs) > 16 {
			return fmt.Errorf("weclaw.send_api.trusted_proxy_cidrs must contain between 1 and 16 networks in proxy mode")
		}
	} else if len(c.TrustedProxyCIDRs) != 0 {
		return fmt.Errorf("weclaw.send_api.trusted_proxy_cidrs requires proxy_mode")
	}
	seenNetworks := make(map[string]bool, len(c.TrustedProxyCIDRs))
	for _, raw := range c.TrustedProxyCIDRs {
		_, network, err := net.ParseCIDR(raw)
		prefixLength, _ := networkMaskSize(network)
		if err != nil || network.String() != raw || prefixLength == 0 || seenNetworks[raw] {
			return fmt.Errorf("weclaw.send_api.trusted_proxy_cidrs contains an invalid or duplicate canonical network")
		}
		seenNetworks[raw] = true
	}
	if c.Enabled && (len(c.Tokens) == 0 || len(c.Tokens) > 16) {
		return fmt.Errorf("weclaw.send_api.tokens must contain between 1 and 16 tokens when enabled")
	}
	seenCallers := make(map[string]bool, len(c.Tokens))
	seenHashes := make(map[string]bool, len(c.Tokens))
	for index, token := range c.Tokens {
		prefix := fmt.Sprintf("weclaw.send_api.tokens[%d]", index)
		if !projectIDPattern.MatchString(token.CallerID) || seenCallers[token.CallerID] {
			return fmt.Errorf("%s.caller_id is invalid or duplicated", prefix)
		}
		seenCallers[token.CallerID] = true
		decodedHash, err := hex.DecodeString(token.TokenSHA256)
		if err != nil || len(decodedHash) != 32 || strings.ToLower(token.TokenSHA256) != token.TokenSHA256 || seenHashes[token.TokenSHA256] {
			return fmt.Errorf("%s.token_sha256 must be a unique lowercase SHA-256 hash", prefix)
		}
		seenHashes[token.TokenSHA256] = true
		if len(token.Scopes) == 0 || len(token.Scopes) > 2 {
			return fmt.Errorf("%s.scopes must contain send:text or send:media", prefix)
		}
		seenScopes := make(map[string]bool, len(token.Scopes))
		for _, scope := range token.Scopes {
			if scope != "send:text" && scope != "send:media" || seenScopes[scope] {
				return fmt.Errorf("%s.scopes contains an invalid or duplicate scope", prefix)
			}
			seenScopes[scope] = true
		}
	}
	return nil
}

func networkMaskSize(network *net.IPNet) (int, int) {
	if network == nil {
		return 0, 0
	}
	return network.Mask.Size()
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
		return fmt.Errorf("weclaw.security.remote_lock_code must be a single line with 6 to 64 characters")
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
		return fmt.Errorf("weclaw.reply.voice.ffmpeg_command must be a clean absolute path")
	}
	if len(c.Providers) == 0 || len(c.Providers) > 4 {
		return fmt.Errorf("weclaw.reply.voice.providers must contain between 1 and 4 providers")
	}
	ids := make(map[string]struct{}, len(c.Providers))
	for index, provider := range c.Providers {
		prefix := fmt.Sprintf("weclaw.reply.voice.providers[%d]", index)
		if !projectIDPattern.MatchString(provider.ID) {
			return fmt.Errorf("%s.id is invalid", prefix)
		}
		if _, exists := ids[provider.ID]; exists {
			return fmt.Errorf("weclaw.reply.voice provider id %q is duplicated", provider.ID)
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
	ID          string `json:"id"`
	Name        string `json:"name"`
	Root        string `json:"root"`
	ServiceName string `json:"service_name,omitempty"`
	HealthURL   string `json:"health_url,omitempty"`
}

var projectIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

func defaultProjectRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(os.TempDir(), "weclaw-workspace")
	}
	return filepath.Join(home, ".weclaw", "workspace")
}

func defaultProjects() []ProjectConfig {
	return []ProjectConfig{{ID: "workspace", Name: "默认项目", Root: defaultProjectRoot()}}
}

func validateProjects(projects []ProjectConfig) error {
	if len(projects) == 0 {
		return fmt.Errorf("weclaw.project_entries must contain at least one project entry")
	}
	ids := make(map[string]struct{}, len(projects))
	names := make(map[string]struct{}, len(projects))
	for index, project := range projects {
		prefix := fmt.Sprintf("weclaw.project_entries[%d]", index)
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
		if project.ServiceName != "" && !serviceNamePattern.MatchString(project.ServiceName) {
			return fmt.Errorf("%s.service_name is invalid", prefix)
		}
		if project.HealthURL != "" {
			healthURL, err := url.Parse(project.HealthURL)
			if err != nil || (healthURL.Scheme != "http" && healthURL.Scheme != "https") || healthURL.Host == "" {
				return fmt.Errorf("%s.health_url must be an absolute HTTP URL", prefix)
			}
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
		return fmt.Errorf("weclaw.reply.visual.browser_command must be an absolute path")
	}
	if c.Enabled && c.LongReplies && (c.LongReplyMinRunes < 300 || c.LongReplyMinRunes > 5000) {
		return fmt.Errorf("weclaw.reply.visual.long_reply_min_runes must be between 300 and 5000")
	}
	return nil
}

var serviceNamePattern = regexp.MustCompile(`^[A-Za-z0-9@_.:-]+$`)

// AutomationConfig 定义完全确定性的项目检查，不把计划任务交给模型自由执行。
type AutomationConfig struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	ProjectID           string   `json:"project_id"`
	DailyAt             string   `json:"daily_at,omitempty"`
	EveryMinutes        int      `json:"every_minutes,omitempty"`
	Timezone            string   `json:"timezone"`
	NotifyOn            string   `json:"notify_on"`
	Checks              []string `json:"checks"`
	CommitLookbackHours int      `json:"commit_lookback_hours"`
}

func validateAutomations(automations []AutomationConfig, projects []ProjectConfig) error {
	projectByID := make(map[string]ProjectConfig, len(projects))
	for _, project := range projects {
		projectByID[project.ID] = project
	}
	ids := make(map[string]struct{}, len(automations))
	for index, automation := range automations {
		prefix := fmt.Sprintf("weclaw.features.automations[%d]", index)
		if !projectIDPattern.MatchString(automation.ID) {
			return fmt.Errorf("%s.id is invalid", prefix)
		}
		if _, exists := ids[automation.ID]; exists {
			return fmt.Errorf("automation id %q is duplicated", automation.ID)
		}
		ids[automation.ID] = struct{}{}
		if strings.TrimSpace(automation.Name) == "" {
			return fmt.Errorf("%s.name is required", prefix)
		}
		if strings.ContainsAny(automation.Name, "\r\n") || len([]rune(automation.Name)) > 40 {
			return fmt.Errorf("%s.name must be a single line with at most 40 characters", prefix)
		}
		project, exists := projectByID[automation.ProjectID]
		if !exists {
			return fmt.Errorf("%s.project_id is not configured", prefix)
		}
		if (automation.DailyAt == "") == (automation.EveryMinutes == 0) {
			return fmt.Errorf("%s must set exactly one of daily_at or every_minutes", prefix)
		}
		if automation.DailyAt != "" {
			if _, err := time.Parse("15:04", automation.DailyAt); err != nil {
				return fmt.Errorf("%s.daily_at must use HH:MM", prefix)
			}
		}
		if automation.EveryMinutes != 0 && (automation.EveryMinutes < 5 || automation.EveryMinutes > 1440) {
			return fmt.Errorf("%s.every_minutes must be between 5 and 1440", prefix)
		}
		if _, err := time.LoadLocation(automation.Timezone); err != nil {
			return fmt.Errorf("%s.timezone is invalid: %w", prefix, err)
		}
		switch automation.NotifyOn {
		case "always", "anomaly", "change", "anomaly_or_change":
		default:
			return fmt.Errorf("%s.notify_on is invalid", prefix)
		}
		if len(automation.Checks) == 0 {
			return fmt.Errorf("%s.checks must not be empty", prefix)
		}
		seenChecks := make(map[string]bool, len(automation.Checks))
		for _, check := range automation.Checks {
			if seenChecks[check] {
				return fmt.Errorf("%s.checks contains duplicate %q", prefix, check)
			}
			seenChecks[check] = true
			switch check {
			case "git":
			case "service":
				if project.ServiceName == "" {
					return fmt.Errorf("%s requires project service_name", prefix)
				}
			case "health":
				if project.HealthURL == "" {
					return fmt.Errorf("%s requires project health_url", prefix)
				}
			default:
				return fmt.Errorf("%s.checks contains unsupported check %q", prefix, check)
			}
		}
		if automation.CommitLookbackHours < 1 || automation.CommitLookbackHours > 168 {
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
		return fmt.Errorf("weclaw.reply.progress.typing_interval_seconds must be between 3 and 30")
	}
	if c.FirstMessageDelaySeconds < 5 || c.FirstMessageDelaySeconds > 120 {
		return fmt.Errorf("weclaw.reply.progress.first_message_delay_seconds must be between 5 and 120")
	}
	if c.MessageIntervalSeconds < 15 || c.MessageIntervalSeconds > 300 {
		return fmt.Errorf("weclaw.reply.progress.message_interval_seconds must be between 15 and 300")
	}
	return nil
}

// DefaultConfig returns an empty configuration.
func DefaultConfig() *Config {
	return &Config{
		SchemaVersion: CurrentSchemaVersion,
		Codex:         defaultCodexConfig(),
		WeClaw: WeClawConfig{
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
	return filepath.Join(home, ".weclaw", "config.json"), nil
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
	if err := validateProjects(c.WeClaw.ProjectEntries); err != nil {
		return err
	}
	if err := c.WeClaw.Reply.Progress.validate(); err != nil {
		return err
	}
	if err := c.WeClaw.Reply.Visual.validate(); err != nil {
		return err
	}
	if c.WeClaw.Reply.Voice.Enabled && !c.WeClaw.Reply.Visual.Enabled {
		return fmt.Errorf("weclaw.reply.voice.enabled requires weclaw.reply.visual.enabled for paired image and audio delivery")
	}
	if err := c.WeClaw.Reply.Voice.validate(); err != nil {
		return err
	}
	if err := c.WeClaw.Features.LinkArchive.validate(); err != nil {
		return err
	}
	if err := validateAutomations(c.WeClaw.Features.Automations, c.WeClaw.ProjectEntries); err != nil {
		return err
	}
	if err := c.WeClaw.Security.validate(); err != nil {
		return err
	}
	return c.WeClaw.SendAPI.validate()
}

func loadEnv(cfg *Config) {
	if v := os.Getenv("WECLAW_SAVE_DIR"); v != "" {
		cfg.WeClaw.Features.LinkArchive.Enabled = true
		cfg.WeClaw.Features.LinkArchive.Directory = v
	}
	if v := os.Getenv("WECLAW_CODEX_COMMAND"); v != "" {
		cfg.Codex.Command = v
	}
	if v := os.Getenv("WECLAW_CODEX_MODEL"); v != "" {
		cfg.Codex.Model = v
	}
	if v := os.Getenv("WECLAW_VISUAL_BROWSER"); v != "" {
		cfg.WeClaw.Reply.Visual.BrowserCommand = v
	}
	if v := os.Getenv("WECLAW_MIMO_API_KEY"); v != "" {
		for index := range cfg.WeClaw.Reply.Voice.Providers {
			if cfg.WeClaw.Reply.Voice.Providers[index].Type == "mimo" && cfg.WeClaw.Reply.Voice.Providers[index].MiMo != nil {
				cfg.WeClaw.Reply.Voice.Providers[index].MiMo.APIKey = v
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
