package config

import (
	"database/sql"
	"fmt"
	"reflect"
	"strconv"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
)

func SetValue(c *Config, key, value string) error {
	key = "config." + strings.TrimPrefix(key, "config.")
	if key == "config.schema" {
		return fmt.Errorf("schema is not editable")
	}
	ptr, ok := scalarSettings(c)[key]
	if !ok {
		return fmt.Errorf("unknown scalar setting %s", key)
	}
	switch p := ptr.(type) {
	case *string:
		*p = value
	case *bool:
		v, err := strconv.ParseBool(value)
		if err != nil {
			return err
		}
		*p = v
	case *int:
		v, err := strconv.Atoi(value)
		if err != nil {
			return err
		}
		*p = v
	}
	return c.validate()
}

// 标量设置有稳定的键；集合独立成表，不保存配置 JSON。
func scalarSettings(c *Config) map[string]any {
	r := &c.Clawbot.Reply
	return map[string]any{
		"config.schema": &c.SchemaVersion, "config.codex.command": &c.Codex.Command, "config.codex.model": &c.Codex.Model,
		"config.management.listen": &c.Clawbot.Management.Listen, "config.management.public_url": &c.Clawbot.Management.PublicURL,
		"config.security.remote_lock_code": &c.Clawbot.Security.RemoteLockCode,
		"config.progress.enabled":          &r.Progress.Enabled, "config.progress.typing_interval": &r.Progress.TypingIntervalSeconds, "config.progress.first_delay": &r.Progress.FirstMessageDelaySeconds,
		"config.visual.enabled": &r.Visual.Enabled, "config.visual.browser": &r.Visual.BrowserCommand, "config.visual.long_replies": &r.Visual.LongReplies, "config.visual.min_runes": &r.Visual.LongReplyMinRunes,
		"config.voice.enabled": &r.Voice.Enabled, "config.voice.ffmpeg": &r.Voice.FFmpegCommand,
	}
}

type workspaceRow struct {
	ProjectConfig
	Position int
}
type providerRow struct {
	ID             string `json:"id"`
	Position       int
	Type           string `json:"type"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Command        string
	Model          string
	ModelConfig    string  `json:"model_config"`
	LengthScale    float64 `json:"length_scale"`
	BaseURL        string  `json:"base_url"`
	APIKey         string  `json:"api_key"`
	Voice          string
	StylePrompt    string `json:"style_prompt"`
}

func LoadRoot(root string) (*Config, error) {
	c := DefaultConfig()
	found := false
	err := storage.View(root, func(tx *sql.Tx) error {
		settings, err := storage.Rows[storage.Setting](tx, "SELECT * FROM settings WHERE key LIKE 'config.%'")
		if err != nil {
			return err
		}
		if len(settings) == 0 {
			return nil
		}
		found = true
		pointers := scalarSettings(c)
		if len(settings) != len(pointers) {
			return fmt.Errorf("incomplete configuration in SQLite")
		}
		for _, setting := range settings {
			ptr, ok := pointers[setting.Key]
			if !ok {
				return fmt.Errorf("unknown setting %s", setting.Key)
			}
			switch p := ptr.(type) {
			case *string:
				*p = setting.Value
			case *bool:
				value, err := strconv.ParseBool(setting.Value)
				if err != nil {
					return err
				}
				*p = value
			case *int:
				value, err := strconv.Atoi(setting.Value)
				if err != nil {
					return err
				}
				*p = value
			}
		}
		workspaces, err := storage.Rows[workspaceRow](tx, "SELECT * FROM workspaces ORDER BY position")
		if err != nil {
			return err
		}
		c.Clawbot.ProjectEntries = nil
		for _, row := range workspaces {
			c.Clawbot.ProjectEntries = append(c.Clawbot.ProjectEntries, row.ProjectConfig)
		}
		env, err := storage.Rows[storage.Setting](tx, "SELECT * FROM codex_env")
		if err != nil {
			return err
		}
		c.Codex.Env = map[string]string{}
		for _, row := range env {
			c.Codex.Env[row.Key] = row.Value
		}
		providers, err := storage.Rows[providerRow](tx, "SELECT * FROM voice_providers ORDER BY position")
		if err != nil {
			return err
		}
		for _, row := range providers {
			p := VoiceProviderConfig{ID: row.ID, Type: row.Type, TimeoutSeconds: row.TimeoutSeconds}
			switch p.Type {
			case "piper":
				p.Piper = &PiperVoiceProviderConfig{Command: row.Command, Model: row.Model, ModelConfig: row.ModelConfig, LengthScale: row.LengthScale}
			case "mimo":
				p.MiMo = &MiMoVoiceProviderConfig{BaseURL: row.BaseURL, APIKey: row.APIKey, Model: row.Model, Voice: row.Voice, StylePrompt: row.StylePrompt}
			default:
				return fmt.Errorf("unknown voice provider type")
			}
			c.Clawbot.Reply.Voice.Providers = append(c.Clawbot.Reply.Voice.Providers, p)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	if !found {
		// 首次初始化也要在写事务内复查，避免覆盖同时提交的新配置。
		if err := storage.Update(root, func(tx *sql.Tx) error {
			var count int
			if err := tx.QueryRow("SELECT count(*) FROM settings WHERE key LIKE 'config.%'").Scan(&count); err != nil {
				return err
			}
			if count == 0 {
				return saveConfig(tx, c)
			}
			return nil
		}); err != nil {
			return nil, err
		}
		return LoadRoot(root)
	}
	return c, nil
}

func SaveRoot(root string, c *Config) error {
	if c == nil {
		return fmt.Errorf("config is required")
	}
	if err := c.validate(); err != nil {
		return err
	}
	return storage.Update(root, func(tx *sql.Tx) error { return saveConfig(tx, c) })
}

func saveConfig(tx *sql.Tx, c *Config) error {
	for key, ptr := range scalarSettings(c) {
		value := fmt.Sprint(reflect.ValueOf(ptr).Elem().Interface())
		if err := storage.Put(tx, "settings", storage.Setting{Key: key, Value: value}); err != nil {
			return err
		}
	}
	for _, table := range []string{"workspaces", "codex_env", "voice_providers"} {
		if _, err := tx.Exec("DELETE FROM " + table); err != nil {
			return err
		}
	}
	for i, workspace := range c.Clawbot.ProjectEntries {
		if err := storage.Put(tx, "workspaces", workspaceRow{workspace, i}); err != nil {
			return err
		}
	}
	for key, value := range c.Codex.Env {
		if err := storage.Put(tx, "codex_env", storage.Setting{Key: key, Value: value}); err != nil {
			return err
		}
	}
	for i, p := range c.Clawbot.Reply.Voice.Providers {
		row := providerRow{ID: p.ID, Position: i, Type: p.Type, TimeoutSeconds: p.TimeoutSeconds}
		if p.Piper != nil {
			row.Command, row.Model, row.ModelConfig, row.LengthScale = p.Piper.Command, p.Piper.Model, p.Piper.ModelConfig, p.Piper.LengthScale
		}
		if p.MiMo != nil {
			row.BaseURL, row.APIKey, row.Model, row.Voice, row.StylePrompt = p.MiMo.BaseURL, p.MiMo.APIKey, p.MiMo.Model, p.MiMo.Voice, p.MiMo.StylePrompt
		}
		if err := storage.Put(tx, "voice_providers", row); err != nil {
			return err
		}
	}
	return nil
}
