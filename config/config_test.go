package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCodexConfigMarshalRoundTrip(t *testing.T) {
	cfg := Config{
		Projects: []ProjectConfig{{ID: "project", Name: "Project", Root: "/srv/project"}},
		Codex: CodexConfig{
			Command: "/usr/local/bin/codex",
			Model:   "gpt-test",
			Env: map[string]string{
				"CODEX_ACCESS_TOKEN": "test-token",
				"EMPTY":              "",
			},
		},
	}

	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}

	var decoded Config
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}

	got := decoded.Codex.Env
	if got["CODEX_ACCESS_TOKEN"] != "test-token" || got["EMPTY"] != "" {
		t.Fatalf("round-trip env = %#v", got)
	}
}

func TestDefaultConfigUsesCodexOnly(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Codex.Command != "codex" {
		t.Fatalf("default codex command = %q", cfg.Codex.Command)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].ID != "workspace" || !filepath.IsAbs(cfg.Projects[0].Root) {
		t.Fatalf("unexpected default projects: %#v", cfg.Projects)
	}
	if !cfg.Progress.Enabled || cfg.Progress.TypingIntervalSeconds != 8 || cfg.Progress.FirstMessageDelaySeconds != 15 || cfg.Progress.MessageIntervalSeconds != 45 {
		t.Fatalf("unexpected default progress config: %#v", cfg.Progress)
	}
	if !cfg.Visual.Enabled || !cfg.Visual.LongReplies || cfg.Visual.LongReplyMinRunes != 900 {
		t.Fatalf("unexpected default visual config: %#v", cfg.Visual)
	}
}

func TestLoadEnvOverridesCodex(t *testing.T) {
	t.Setenv("WECLAW_API_ADDR", "127.0.0.1:18011")
	t.Setenv("WECLAW_CODEX_COMMAND", "/opt/codex")
	t.Setenv("WECLAW_CODEX_MODEL", "gpt-test")
	t.Setenv("WECLAW_VISUAL_BROWSER", "/opt/chromium")

	cfg := DefaultConfig()
	loadEnv(cfg)
	if cfg.APIAddr != "127.0.0.1:18011" {
		t.Fatalf("APIAddr = %q, want %q", cfg.APIAddr, "127.0.0.1:18011")
	}
	if cfg.Codex.Command != "/opt/codex" || cfg.Codex.Model != "gpt-test" {
		t.Fatalf("codex env overrides = %#v", cfg.Codex)
	}
	if cfg.Visual.BrowserCommand != "/opt/chromium" {
		t.Fatalf("visual browser override = %q", cfg.Visual.BrowserCommand)
	}
}

func TestVisualConfigRejectsRelativeBrowserCommand(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Visual.BrowserCommand = "chromium"
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("visual validation error = %v", err)
	}
}

func TestVisualConfigRejectsUnsafeLongReplyThreshold(t *testing.T) {
	for _, threshold := range []int{299, 5001} {
		cfg := DefaultConfig()
		cfg.Visual.LongReplyMinRunes = threshold
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "long_reply_min_runes") {
			t.Fatalf("threshold %d validation error = %v", threshold, err)
		}
	}
}

func TestLoadKeepsVisualDefaultWhenSectionIsOmitted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("WECLAW_VISUAL_BROWSER", "")
	path := filepath.Join(home, ".weclaw", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{
  "progress": {"enabled": true, "typing_interval_seconds": 8, "first_message_delay_seconds": 15, "message_interval_seconds": 45},
  "projects": [{"id": "project", "name": "Project", "root": "/srv/project"}],
  "codex": {"command": "codex", "model": ""}
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Visual.Enabled || !cfg.Visual.LongReplies || cfg.Visual.LongReplyMinRunes != 900 {
		t.Fatalf("omitted visual section should preserve defaults: %#v", cfg.Visual)
	}
}

func TestValidateProjects(t *testing.T) {
	projects := []ProjectConfig{{
		ID: "weclaw", Name: "WeClaw", Root: "/srv/weclaw",
		ServiceName: "weclaw.service", HealthURL: "http://127.0.0.1:18011/health",
		QuickTasks: []QuickTaskConfig{{ID: "review", Name: "审查改动", Prompt: "审查当前改动"}},
	}}
	if err := validateProjects(projects); err != nil {
		t.Fatalf("validateProjects() error = %v", err)
	}
	projects[0].QuickTasks[0].ID = "Review!"
	if err := validateProjects(projects); err == nil || !strings.Contains(err.Error(), "id") {
		t.Fatalf("invalid quick task error = %v", err)
	}
}

func TestLoadRejectsRemovedCodexCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".weclaw", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"projects":[{"id":"project","name":"Project","root":"/srv/project"}],"codex":{"command":"codex","cwd":"/srv/project"}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Load() error = %v, want removed cwd rejection", err)
	}
}

func TestLoadRejectsLegacyAgentSchema(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".weclaw", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := []byte(`{"default_agent":"codex","agents":{"codex":{"type":"acp","command":"codex"}}}`)
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Load() error = %v, want strict legacy rejection", err)
	}
}

func TestValidateAutomations(t *testing.T) {
	projects := []ProjectConfig{{
		ID: "project", Name: "Project", Root: filepath.Clean("/srv/project"),
		ServiceName: "weclaw.service", HealthURL: "http://127.0.0.1:18011/health",
	}}
	automations := []AutomationConfig{{
		ID: "daily", Name: "项目日报", ProjectID: "project", DailyAt: "09:00",
		Timezone: "Asia/Shanghai", NotifyOn: "anomaly_or_change",
		Checks: []string{"git", "service", "health"}, CommitLookbackHours: 24,
	}}
	if err := validateAutomations(automations, projects); err != nil {
		t.Fatalf("validateAutomations() error: %v", err)
	}
}

func TestValidateAutomationsRejectsImplicitOrUnsafeValues(t *testing.T) {
	projects := []ProjectConfig{{
		ID: "project", Name: "Project", Root: "/srv/project",
		ServiceName: "weclaw.service", HealthURL: "http://127.0.0.1:18011/health",
	}}
	base := AutomationConfig{
		ID: "daily", Name: "项目日报", ProjectID: "project", DailyAt: "09:00",
		Timezone: "Asia/Shanghai", NotifyOn: "always", Checks: []string{"git"}, CommitLookbackHours: 24,
	}
	tests := []struct {
		name   string
		mutate func(*AutomationConfig)
		want   string
	}{
		{name: "time", mutate: func(automation *AutomationConfig) { automation.DailyAt = "9am" }, want: "daily_at"},
		{name: "timezone", mutate: func(automation *AutomationConfig) { automation.Timezone = "Mars/Base" }, want: "timezone"},
		{name: "unknown project", mutate: func(automation *AutomationConfig) { automation.ProjectID = "missing" }, want: "project_id"},
		{name: "invalid policy", mutate: func(automation *AutomationConfig) { automation.NotifyOn = "sometimes" }, want: "notify_on"},
		{name: "missing schedule", mutate: func(automation *AutomationConfig) { automation.DailyAt = "" }, want: "exactly one"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			automation := base
			test.mutate(&automation)
			err := validateAutomations([]AutomationConfig{automation}, projects)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestSecurityAndVoiceConfigurationIsStrict(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Security.RemoteLockCode = "short"
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "remote_lock_code") {
		t.Fatalf("short lock code error = %v", err)
	}
	cfg.Security.RemoteLockCode = "secure-code"
	cfg.Voice = VoiceConfig{Enabled: true, Command: "tts"}
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "voice.command") {
		t.Fatalf("relative voice command error = %v", err)
	}
	cfg.Voice.Command = "/usr/local/bin/weclaw-tts"
	if err := cfg.validate(); err != nil {
		t.Fatalf("valid security and voice config: %v", err)
	}
}
