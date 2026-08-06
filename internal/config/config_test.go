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
	if cfg.Voice.Enabled || cfg.Voice.FFmpegCommand != "" || len(cfg.Voice.Providers) != 0 {
		t.Fatalf("unexpected default voice config: %#v", cfg.Voice)
	}
}

func TestLoadEnvOverridesCodex(t *testing.T) {
	t.Setenv("WECLAW_CODEX_COMMAND", "/opt/codex")
	t.Setenv("WECLAW_CODEX_MODEL", "gpt-test")
	t.Setenv("WECLAW_VISUAL_BROWSER", "/opt/chromium")
	t.Setenv("WECLAW_MIMO_API_KEY", "mimo-test-key")

	cfg := DefaultConfig()
	cfg.Voice.Providers = []VoiceProviderConfig{{
		ID: "mimo", Type: "mimo", TimeoutSeconds: 90,
		MiMo: &MiMoVoiceProviderConfig{BaseURL: "https://api.xiaomimimo.com/v1", Model: "mimo-v2.5-tts", Voice: "茉莉"},
	}}
	loadEnv(cfg)
	if cfg.Codex.Command != "/opt/codex" || cfg.Codex.Model != "gpt-test" {
		t.Fatalf("codex env overrides = %#v", cfg.Codex)
	}
	if cfg.Visual.BrowserCommand != "/opt/chromium" {
		t.Fatalf("visual browser override = %q", cfg.Visual.BrowserCommand)
	}
	if cfg.Voice.Providers[0].MiMo.APIKey != "mimo-test-key" {
		t.Fatalf("MiMo API key override was not applied")
	}
}

func TestSendAPIDefaultsToDisabled(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.SendAPI.Enabled || cfg.SendAPI.ListenAddr != "" || len(cfg.SendAPI.Tokens) != 0 {
		t.Fatalf("unexpected default send API config: %#v", cfg.SendAPI)
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("default config validation: %v", err)
	}
}

func TestSendAPIRequiresExplicitSecurityBoundary(t *testing.T) {
	validToken := SendAPITokenConfig{
		CallerID:    "dashboard",
		TokenSHA256: strings.Repeat("a", 64),
		Scopes:      []string{"send:text", "send:media"},
	}
	tests := []struct {
		name   string
		config SendAPIConfig
	}{
		{name: "missing address", config: SendAPIConfig{Enabled: true, Tokens: []SendAPITokenConfig{validToken}}},
		{name: "public plaintext", config: SendAPIConfig{Enabled: true, ListenAddr: "0.0.0.0:18012", Tokens: []SendAPITokenConfig{validToken}}},
		{name: "proxy without trust", config: SendAPIConfig{Enabled: true, ListenAddr: "0.0.0.0:18012", ProxyMode: true, Tokens: []SendAPITokenConfig{validToken}}},
		{name: "proxy trusts everyone", config: SendAPIConfig{Enabled: true, ListenAddr: "0.0.0.0:18012", ProxyMode: true, TrustedProxyCIDRs: []string{"0.0.0.0/0"}, Tokens: []SendAPITokenConfig{validToken}}},
		{name: "missing tokens", config: SendAPIConfig{Enabled: true, ListenAddr: "127.0.0.1:18012"}},
		{name: "plaintext token", config: SendAPIConfig{Enabled: true, ListenAddr: "127.0.0.1:18012", Tokens: []SendAPITokenConfig{{CallerID: "dashboard", TokenSHA256: "secret", Scopes: []string{"send:text"}}}}},
		{name: "unknown scope", config: SendAPIConfig{Enabled: true, ListenAddr: "127.0.0.1:18012", Tokens: []SendAPITokenConfig{{CallerID: "dashboard", TokenSHA256: strings.Repeat("b", 64), Scopes: []string{"admin"}}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.SendAPI = test.config
			if err := cfg.validate(); err == nil {
				t.Fatal("unsafe send API config was accepted")
			}
		})
	}

	for _, valid := range []SendAPIConfig{
		{Enabled: true, ListenAddr: "127.0.0.1:18012", Tokens: []SendAPITokenConfig{validToken}},
		{Enabled: true, ListenAddr: "0.0.0.0:18012", ProxyMode: true, TrustedProxyCIDRs: []string{"10.10.0.0/16"}, Tokens: []SendAPITokenConfig{validToken}},
	} {
		cfg := DefaultConfig()
		cfg.SendAPI = valid
		if err := cfg.validate(); err != nil {
			t.Fatalf("valid send API config rejected: %v", err)
		}
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
	path := filepath.Join(home, ".weclaw", "config.json")
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
	cfg.Voice.Enabled = true
	cfg.Voice.FFmpegCommand = "/usr/bin/ffmpeg"
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "voice.providers") {
		t.Fatalf("missing providers error = %v", err)
	}
	cfg.Voice.Providers = []VoiceProviderConfig{{
		ID: "mimo", Type: "mimo", TimeoutSeconds: 90,
		MiMo: &MiMoVoiceProviderConfig{BaseURL: "http://example.com/v1", APIKey: "test-key", Model: "mimo-v2.5-tts", Voice: "茉莉"},
	}}
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("insecure base URL error = %v", err)
	}
	mimo := cfg.Voice.Providers[0].MiMo
	mimo.BaseURL = "https://api.xiaomimimo.com/v1"
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
	cfg.Voice.Providers = append([]VoiceProviderConfig{{
		ID: "local", Type: "piper", TimeoutSeconds: 30,
		Piper: &PiperVoiceProviderConfig{
			Command: "/opt/piper/bin/piper", Model: "/opt/piper/voice.onnx", ModelConfig: "/opt/piper/voice.onnx.json",
			LengthScale: 1,
		},
	}}, cfg.Voice.Providers...)
	if err := cfg.validate(); err != nil {
		t.Fatalf("valid security and voice config: %v", err)
	}
	cfg.Voice.Providers[1].ID = "local"
	if err := cfg.validate(); err == nil || !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("duplicate provider error = %v", err)
	}
}

func TestVoiceRequiresVisualDelivery(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Visual.Enabled = false
	cfg.Voice = VoiceConfig{
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
	path := filepath.Join(home, ".weclaw", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"projects":[{"id":"project","name":"Project","root":"/srv/project"}],"codex":{"command":"codex"},"voice":{"enabled":true,"base_url":"https://api.xiaomimimo.com/v1","api_key":"test","model":"mimo-v2.5-tts","voice":"茉莉"}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("Load() error = %v, want removed single-provider voice schema rejection", err)
	}
}

func TestDecodeRejectsRemovedSilkCommand(t *testing.T) {
	data := []byte(`{"voice":{"silk_command":"/usr/local/bin/weclaw-silk-encoder"}}`)
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
