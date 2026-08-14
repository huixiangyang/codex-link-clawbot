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
		SchemaVersion: CurrentSchemaVersion,
		Clawbot: ClawbotConfig{
			ProjectEntries: []ProjectConfig{{ID: "project", Name: "Project", Root: "/srv/project"}},
		},
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
	if cfg.SchemaVersion != CurrentSchemaVersion {
		t.Fatalf("unexpected schema version: %d", cfg.SchemaVersion)
	}
	entries := cfg.Clawbot.ProjectEntries
	reply := cfg.Clawbot.Reply
	if len(entries) != 1 || entries[0].ID != "workspace" || !filepath.IsAbs(entries[0].Root) {
		t.Fatalf("unexpected default project entries: %#v", entries)
	}
	if !reply.Progress.Enabled || reply.Progress.TypingIntervalSeconds != 8 || reply.Progress.FirstMessageDelaySeconds != 15 {
		t.Fatalf("unexpected default progress config: %#v", reply.Progress)
	}
	if !reply.Visual.Enabled || !reply.Visual.LongReplies || reply.Visual.LongReplyMinRunes != 900 {
		t.Fatalf("unexpected default visual config: %#v", reply.Visual)
	}
	if reply.Voice.Enabled || reply.Voice.FFmpegCommand != "" || len(reply.Voice.Providers) != 0 {
		t.Fatalf("unexpected default voice config: %#v", reply.Voice)
	}
}

func TestLoadEnvOverridesCodex(t *testing.T) {
	t.Setenv("CODEX_LINK_CLAWBOT_CODEX_COMMAND", "/opt/codex")
	t.Setenv("CODEX_LINK_CLAWBOT_CODEX_MODEL", "gpt-test")
	t.Setenv("CODEX_LINK_CLAWBOT_VISUAL_BROWSER", "/opt/chromium")
	t.Setenv("CODEX_LINK_CLAWBOT_MIMO_API_KEY", "mimo-test-key")

	cfg := DefaultConfig()
	cfg.Clawbot.Reply.Voice.Providers = []VoiceProviderConfig{{
		ID: "mimo", Type: "mimo", TimeoutSeconds: 90,
		MiMo: &MiMoVoiceProviderConfig{BaseURL: "https://management.xiaomimimo.com/v1", Model: "mimo-v2.5-tts", Voice: "茉莉"},
	}}
	loadEnv(cfg)
	if cfg.Codex.Command != "/opt/codex" || cfg.Codex.Model != "gpt-test" {
		t.Fatalf("codex env overrides = %#v", cfg.Codex)
	}
	if cfg.Clawbot.Reply.Visual.BrowserCommand != "/opt/chromium" {
		t.Fatalf("visual browser override = %q", cfg.Clawbot.Reply.Visual.BrowserCommand)
	}
	if cfg.Clawbot.Reply.Voice.Providers[0].MiMo.APIKey != "mimo-test-key" {
		t.Fatalf("MiMo API key override was not applied")
	}
}

func TestVisualConfigRejectsRelativeBrowserCommand(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Clawbot.Reply.Visual.BrowserCommand = "chromium"
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("visual validation error = %v", err)
	}
}

func TestVisualConfigRejectsUnsafeLongReplyThreshold(t *testing.T) {
	for _, threshold := range []int{299, 5001} {
		cfg := DefaultConfig()
		cfg.Clawbot.Reply.Visual.LongReplyMinRunes = threshold
		if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "long_reply_min_runes") {
			t.Fatalf("threshold %d validation error = %v", threshold, err)
		}
	}
}

func TestLoadKeepsVisualDefaultWhenSectionIsOmitted(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_LINK_CLAWBOT_VISUAL_BROWSER", "")
	path := filepath.Join(home, ".codex-link-clawbot", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{
  "schema_version": 6,
  "codex": {"command": "codex", "model": ""},
  "codex-link-clawbot": {
    "project_entries": [{"id": "project", "name": "Project", "root": "/srv/project"}],
    "reply": {
      "progress": {"enabled": true, "typing_interval_seconds": 8, "first_message_delay_seconds": 15}
    },
    "security": {}
  }
}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Clawbot.Reply.Visual.Enabled || !cfg.Clawbot.Reply.Visual.LongReplies || cfg.Clawbot.Reply.Visual.LongReplyMinRunes != 900 {
		t.Fatalf("omitted visual section should preserve defaults: %#v", cfg.Clawbot.Reply.Visual)
	}
}

func TestLoadRejectsFlatConfigurationSchema(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".codex-link-clawbot", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"projects":[{"id":"project","name":"Project","root":"/srv/project"}],"codex":{"command":"codex"}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Load() error = %v, want flat schema rejection", err)
	}
}

func TestDecodeRejectsRetiredTimedProgressInterval(t *testing.T) {
	_, err := decodeConfig([]byte(`{"schema_version":6,"codex":{"command":"codex"},"codex-link-clawbot":{"project_entries":[{"id":"project","name":"Project","root":"/srv/project"}],"reply":{"progress":{"enabled":true,"typing_interval_seconds":8,"first_message_delay_seconds":15,"message_interval_seconds":45}},"security":{}}}`))
	if err == nil || !strings.Contains(err.Error(), "message_interval_seconds") {
		t.Fatalf("decodeConfig() error = %v, want retired interval rejection", err)
	}
}

func TestDecodeRequiresExplicitSchemaVersion(t *testing.T) {
	if _, err := decodeConfig([]byte(`{"codex":{"command":"codex"},"codex-link-clawbot":{}}`)); err == nil || !strings.Contains(err.Error(), "schema_version is required") {
		t.Fatalf("decodeConfig() error = %v", err)
	}
}

func TestValidateProjects(t *testing.T) {
	projects := []ProjectConfig{{
		ID: "codex-link-clawbot", Name: "codex-link-clawbot", Root: "/srv/codex-link-clawbot",
	}}
	if err := validateProjects(projects); err != nil {
		t.Fatalf("validateProjects() error = %v", err)
	}
	projects[0].ID = "Review!"
	if err := validateProjects(projects); err == nil || !strings.Contains(err.Error(), "id") {
		t.Fatalf("invalid project error = %v", err)
	}
}

func TestLoadRejectsRemovedProjectQuickTasks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".codex-link-clawbot", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"projects":[{"id":"project","name":"Project","root":"/srv/project","quick_tasks":[{"id":"review","name":"Review","prompt":"Review changes"}]}],"codex":{"command":"codex"}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Load() error = %v, want removed quick_tasks rejection", err)
	}
}

func TestDecodeRejectsRemovedProjectMonitoringFields(t *testing.T) {
	for _, data := range []string{
		`{"schema_version":6,"codex":{"command":"codex"},"codex-link-clawbot":{"project_entries":[{"id":"project","name":"Project","root":"/srv/project","service_name":"app.service"}],"reply":{},"security":{}}}`,
		`{"schema_version":6,"codex":{"command":"codex"},"codex-link-clawbot":{"project_entries":[{"id":"project","name":"Project","root":"/srv/project","health_url":"http://127.0.0.1/health"}],"reply":{},"security":{}}}`,
		`{"schema_version":6,"codex":{"command":"codex"},"codex-link-clawbot":{"project_entries":[{"id":"project","name":"Project","root":"/srv/project"}],"project_watches":[],"reply":{},"security":{}}}`,
	} {
		if _, err := decodeConfig([]byte(data)); err == nil || !strings.Contains(err.Error(), "unknown field") {
			t.Fatalf("decodeConfig() error = %v, want removed project monitoring field rejection", err)
		}
	}
}

func TestLoadRejectsRemovedCodexCwd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".codex-link-clawbot", "config.json")
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
	path := filepath.Join(home, ".codex-link-clawbot", "config.json")
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

func TestSecurityAndVoiceConfigurationIsStrict(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Clawbot.Security.RemoteLockCode = "short"
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "remote_lock_code") {
		t.Fatalf("short lock code error = %v", err)
	}
	cfg.Clawbot.Security.RemoteLockCode = "secure-code"
	cfg.Clawbot.Reply.Voice.Enabled = true
	cfg.Clawbot.Reply.Voice.FFmpegCommand = "/usr/bin/ffmpeg"
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "voice.providers") {
		t.Fatalf("missing providers error = %v", err)
	}
	cfg.Clawbot.Reply.Voice.Providers = []VoiceProviderConfig{{
		ID: "mimo", Type: "mimo", TimeoutSeconds: 90,
		MiMo: &MiMoVoiceProviderConfig{BaseURL: "http://example.com/v1", APIKey: "test-key", Model: "mimo-v2.5-tts", Voice: "茉莉"},
	}}
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("insecure base URL error = %v", err)
	}
	mimo := cfg.Clawbot.Reply.Voice.Providers[0].MiMo
	mimo.BaseURL = "https://management.xiaomimimo.com/v1"
	mimo.Model = "mimo-v2.5-tts-voiceclone"
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), ".model") {
		t.Fatalf("unsupported model error = %v", err)
	}
	mimo.Model = "mimo-v2.5-tts"
	mimo.Voice = "unknown"
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), ".voice") {
		t.Fatalf("unsupported voice error = %v", err)
	}
	mimo.Voice = "茉莉"
	cfg.Clawbot.Reply.Voice.Providers = append([]VoiceProviderConfig{{
		ID: "local", Type: "piper", TimeoutSeconds: 30,
		Piper: &PiperVoiceProviderConfig{
			Command: "/opt/piper/bin/piper", Model: "/opt/piper/voice.onnx", ModelConfig: "/opt/piper/voice.onnx.json",
			LengthScale: 1,
		},
	}}, cfg.Clawbot.Reply.Voice.Providers...)
	if err := cfg.validate(); err != nil {
		t.Fatalf("valid security and voice config: %v", err)
	}
	cfg.Clawbot.Reply.Voice.Providers[1].ID = "local"
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate provider error = %v", err)
	}
}

func TestVoiceRequiresVisualDelivery(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Clawbot.Reply.Visual.Enabled = false
	cfg.Clawbot.Reply.Voice = VoiceConfig{
		Enabled: true, FFmpegCommand: "/usr/bin/ffmpeg",
		Providers: []VoiceProviderConfig{{
			ID: "local", Type: "piper", TimeoutSeconds: 30,
			Piper: &PiperVoiceProviderConfig{
				Command: "/opt/piper/bin/piper", Model: "/opt/piper/voice.onnx", ModelConfig: "/opt/piper/voice.onnx.json", LengthScale: 1,
			},
		}},
	}
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "visual.enabled") {
		t.Fatalf("validation error = %v, want paired visual requirement", err)
	}
}

func TestLoadRejectsRemovedSingleProviderVoiceSchema(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".codex-link-clawbot", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"projects":[{"id":"project","name":"Project","root":"/srv/project"}],"codex":{"command":"codex"},"voice":{"enabled":true,"base_url":"https://management.xiaomimimo.com/v1","api_key":"test","model":"mimo-v2.5-tts","voice":"茉莉"}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Load() error = %v, want removed single-provider voice schema rejection", err)
	}
}

func TestDecodeRejectsRemovedSilkCommand(t *testing.T) {
	data := []byte(`{"voice":{"silk_command":"/usr/local/bin/codex-link-clawbot-silk-encoder"}}`)
	if _, err := decodeConfig(data); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("decodeConfig() error = %v, want removed silk_command rejection", err)
	}
}

func TestDecodeRejectsRemovedUnauthenticatedAPIAddress(t *testing.T) {
	data := []byte(`{"api_addr":"127.0.0.1:18011"}`)
	if _, err := decodeConfig(data); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("decodeConfig() error = %v, want removed api_addr rejection", err)
	}
}
