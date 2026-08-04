package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentConfigUnmarshalEnv(t *testing.T) {
	var cfg Config
	data := []byte(`{
		"agents": {
			"claude": {
				"type": "cli",
				"command": "claude",
				"env": {
					"ANTHROPIC_API_KEY": "test-key",
					"EMPTY": ""
				}
			}
		}
	}`)

	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	ag, ok := cfg.Agents["claude"]
	if !ok {
		t.Fatalf("expected claude agent config")
	}
	if got := ag.Env["ANTHROPIC_API_KEY"]; got != "test-key" {
		t.Fatalf("ANTHROPIC_API_KEY = %q, want %q", got, "test-key")
	}
	if got, ok := ag.Env["EMPTY"]; !ok || got != "" {
		t.Fatalf("EMPTY = %q, present=%v; want empty string present", got, ok)
	}
}

func TestAgentConfigMarshalEnvRoundTrip(t *testing.T) {
	cfg := Config{
		Agents: map[string]AgentConfig{
			"claude": {
				Type:    "cli",
				Command: "claude",
				Env: map[string]string{
					"ANTHROPIC_API_KEY": "test-key",
					"EMPTY":             "",
				},
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

	got := decoded.Agents["claude"].Env
	if got["ANTHROPIC_API_KEY"] != "test-key" || got["EMPTY"] != "" {
		t.Fatalf("round-trip env = %#v", got)
	}
}

func TestAgentConfigWithoutEnvStillLoads(t *testing.T) {
	var cfg Config
	data := []byte(`{
		"agents": {
			"claude": {
				"type": "cli",
				"command": "claude"
			}
		}
	}`)

	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal config without env: %v", err)
	}

	if cfg.Agents["claude"].Env != nil {
		t.Fatalf("Env = %#v, want nil", cfg.Agents["claude"].Env)
	}
}

func TestDefaultConfigInitializesAgentsMap(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Agents == nil {
		t.Fatal("DefaultConfig() Agents = nil, want initialized map")
	}
	if !cfg.Progress.Enabled || cfg.Progress.TypingIntervalSeconds != 8 || cfg.Progress.FirstMessageDelaySeconds != 15 || cfg.Progress.MessageIntervalSeconds != 45 {
		t.Fatalf("unexpected default progress config: %#v", cfg.Progress)
	}
}

func TestBuildAliasMapRejectsTaskControlCommands(t *testing.T) {
	aliases := BuildAliasMap(map[string]AgentConfig{
		"codex": {
			Aliases: []string{"status", "cancel", "work"},
		},
	})

	if _, ok := aliases["status"]; ok {
		t.Fatal("status must remain reserved for task control")
	}
	if _, ok := aliases["cancel"]; ok {
		t.Fatal("cancel must remain reserved for task control")
	}
	if got := aliases["work"]; got != "codex" {
		t.Fatalf("ordinary alias = %q, want codex", got)
	}
}

func TestLoadEnvOverridesTopLevelOnly(t *testing.T) {
	t.Setenv("WECLAW_DEFAULT_AGENT", "codex")
	t.Setenv("WECLAW_API_ADDR", "127.0.0.1:18011")

	cfg := DefaultConfig()
	cfg.Agents["claude"] = AgentConfig{
		Type: "cli",
		Env: map[string]string{
			"KEEP": "value",
		},
	}

	loadEnv(cfg)

	if cfg.DefaultAgent != "codex" {
		t.Fatalf("DefaultAgent = %q, want %q", cfg.DefaultAgent, "codex")
	}
	if cfg.APIAddr != "127.0.0.1:18011" {
		t.Fatalf("APIAddr = %q, want %q", cfg.APIAddr, "127.0.0.1:18011")
	}
	if got := cfg.Agents["claude"].Env["KEEP"]; got != "value" {
		t.Fatalf("agent env = %q, want preserved value", got)
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
