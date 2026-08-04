package config

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Config holds the application configuration.
type Config struct {
	DefaultAgent     string                  `json:"default_agent"`
	APIAddr          string                  `json:"api_addr,omitempty"`
	SaveDir          string                  `json:"save_dir,omitempty"` // Linkhoard archive directory
	Progress         ProgressConfig          `json:"progress"`
	ScheduledReports []ScheduledReportConfig `json:"scheduled_reports,omitempty"`
	Agents           map[string]AgentConfig  `json:"agents"`
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

// AgentConfig holds configuration for a single agent.
type AgentConfig struct {
	Type         string            `json:"type"`                    // "acp", "cli", or "http"
	Command      string            `json:"command,omitempty"`       // binary path (cli/acp type)
	Args         []string          `json:"args,omitempty"`          // extra args for command (e.g. ["acp"] for cursor)
	Aliases      []string          `json:"aliases,omitempty"`       // custom trigger commands (e.g. ["gpt", "4o"])
	Cwd          string            `json:"cwd,omitempty"`           // working directory (workspace)
	Env          map[string]string `json:"env,omitempty"`           // extra environment variables (cli/acp type)
	Model        string            `json:"model,omitempty"`         // model name
	SystemPrompt string            `json:"system_prompt,omitempty"` // system prompt
	Endpoint     string            `json:"endpoint,omitempty"`      // API endpoint (http type)
	APIKey       string            `json:"api_key,omitempty"`       // API key (http type)
	Headers      map[string]string `json:"headers,omitempty"`       // extra HTTP headers (http type)
	MaxHistory   int               `json:"max_history,omitempty"`   // max history (http type)
}

// BuildAliasMap builds a map from custom alias to agent name from all agent configs.
// It logs warnings for conflicts: duplicate aliases and aliases shadowing agent keys.
func BuildAliasMap(agents map[string]AgentConfig) map[string]string {
	// Built-in commands that cannot be overridden
	reserved := map[string]bool{
		"status": true, "cancel": true, "info": true, "help": true,
		"new": true, "clear": true, "cwd": true,
	}

	m := make(map[string]string)
	for name, cfg := range agents {
		for _, alias := range cfg.Aliases {
			if reserved[alias] {
				log.Printf("[config] WARNING: alias %q for agent %q conflicts with built-in command, ignored", alias, name)
				continue
			}
			if existing, ok := m[alias]; ok {
				log.Printf("[config] WARNING: alias %q is defined by both %q and %q, using %q", alias, existing, name, name)
			}
			m[alias] = name
		}
	}

	// Warn if a custom alias shadows an agent key
	for alias, target := range m {
		if _, isAgent := agents[alias]; isAgent && alias != target {
			log.Printf("[config] WARNING: alias %q (-> %q) shadows agent key %q", alias, target, alias)
		}
	}

	return m
}

// DefaultConfig returns an empty configuration.
func DefaultConfig() *Config {
	return &Config{
		Progress: defaultProgressConfig(),
		Agents:   make(map[string]AgentConfig),
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
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Agents == nil {
		cfg.Agents = make(map[string]AgentConfig)
	}
	if err := cfg.Progress.validate(); err != nil {
		return nil, err
	}
	if err := validateScheduledReports(cfg.ScheduledReports); err != nil {
		return nil, err
	}

	loadEnv(cfg)
	return cfg, nil
}

func loadEnv(cfg *Config) {
	if v := os.Getenv("WECLAW_DEFAULT_AGENT"); v != "" {
		cfg.DefaultAgent = v
	}
	if v := os.Getenv("WECLAW_API_ADDR"); v != "" {
		cfg.APIAddr = v
	}
	if v := os.Getenv("WECLAW_SAVE_DIR"); v != "" {
		cfg.SaveDir = v
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
