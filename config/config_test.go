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
		Codex: CodexConfig{
			Command: "/usr/local/bin/codex",
			Cwd:     "/srv/project",
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
	if !cfg.Progress.Enabled || cfg.Progress.TypingIntervalSeconds != 8 || cfg.Progress.FirstMessageDelaySeconds != 15 || cfg.Progress.MessageIntervalSeconds != 45 {
		t.Fatalf("unexpected default progress config: %#v", cfg.Progress)
	}
}

func TestLoadEnvOverridesCodex(t *testing.T) {
	t.Setenv("WECLAW_API_ADDR", "127.0.0.1:18011")
	t.Setenv("WECLAW_CODEX_COMMAND", "/opt/codex")
	t.Setenv("WECLAW_CODEX_CWD", "/srv/project")
	t.Setenv("WECLAW_CODEX_MODEL", "gpt-test")

	cfg := DefaultConfig()
	loadEnv(cfg)
	if cfg.APIAddr != "127.0.0.1:18011" {
		t.Fatalf("APIAddr = %q, want %q", cfg.APIAddr, "127.0.0.1:18011")
	}
	if cfg.Codex.Command != "/opt/codex" || cfg.Codex.Cwd != "/srv/project" || cfg.Codex.Model != "gpt-test" {
		t.Fatalf("codex env overrides = %#v", cfg.Codex)
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

func TestValidateScheduledReports(t *testing.T) {
	reports := []ScheduledReportConfig{{
		Name:                "项目日报",
		DailyAt:             "09:00",
		Timezone:            "Asia/Shanghai",
		ProjectDir:          filepath.Clean("/srv/project"),
		ServiceName:         "weclaw.service",
		HealthURL:           "http://127.0.0.1:18011/health",
		CommitLookbackHours: 24,
	}}
	if err := validateScheduledReports(reports); err != nil {
		t.Fatalf("validateScheduledReports() error: %v", err)
	}
}

func TestValidateScheduledReportsRejectsImplicitOrUnsafeValues(t *testing.T) {
	base := ScheduledReportConfig{
		Name: "项目日报", DailyAt: "09:00", Timezone: "Asia/Shanghai",
		ProjectDir: "/srv/project", ServiceName: "weclaw.service",
		HealthURL: "http://127.0.0.1:18011/health", CommitLookbackHours: 24,
	}
	tests := []struct {
		name   string
		mutate func(*ScheduledReportConfig)
		want   string
	}{
		{name: "time", mutate: func(report *ScheduledReportConfig) { report.DailyAt = "9am" }, want: "daily_at"},
		{name: "timezone", mutate: func(report *ScheduledReportConfig) { report.Timezone = "Mars/Base" }, want: "timezone"},
		{name: "relative project", mutate: func(report *ScheduledReportConfig) { report.ProjectDir = "project" }, want: "absolute"},
		{name: "service injection", mutate: func(report *ScheduledReportConfig) { report.ServiceName = "weclaw;reboot" }, want: "service_name"},
		{name: "health URL", mutate: func(report *ScheduledReportConfig) { report.HealthURL = "file:///etc/passwd" }, want: "health_url"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := base
			test.mutate(&report)
			err := validateScheduledReports([]ScheduledReportConfig{report})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}
}
