package cli

import (
	"encoding/json"

	appconfig "github.com/huixiangyang/weclaw/internal/config"
	"github.com/spf13/cobra"
)

type configurationStatus struct {
	Status        string                    `json:"status"`
	SchemaVersion int                       `json:"schema_version"`
	Path          string                    `json:"path"`
	Codex         codexConfigurationStatus  `json:"codex"`
	WeClaw        weClawConfigurationStatus `json:"weclaw"`
}

type codexConfigurationStatus struct {
	Command      string `json:"command"`
	DefaultModel string `json:"default_model,omitempty"`
}

type weClawConfigurationStatus struct {
	ProjectEntries []projectEntryStatus     `json:"project_entries"`
	Reply          replyConfigurationStatus `json:"reply"`
	Features       featureStatus            `json:"features"`
	Security       securityStatus           `json:"security"`
	SendAPI        sendAPIStatus            `json:"send_api"`
}

type projectEntryStatus struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	ServiceCheck bool   `json:"service_check"`
	HealthCheck  bool   `json:"health_check"`
}

type replyConfigurationStatus struct {
	Progress       bool   `json:"progress"`
	Visual         bool   `json:"visual"`
	LongReplies    bool   `json:"long_replies"`
	Voice          bool   `json:"voice"`
	VoiceProviders int    `json:"voice_providers"`
	BrowserMode    string `json:"browser_mode"`
}

type featureStatus struct {
	LinkArchive bool `json:"link_archive"`
	Automations int  `json:"automations"`
}

type securityStatus struct {
	RemoteLock bool `json:"remote_lock"`
}

type sendAPIStatus struct {
	Enabled bool `json:"enabled"`
	Callers int  `json:"callers"`
}

func init() {
	rootCmd.AddCommand(configCmd)
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Validate and summarize the effective configuration",
	Args:  cobra.NoArgs,
	RunE:  runConfigStatus,
}

func runConfigStatus(cmd *cobra.Command, _ []string) error {
	cfg, err := appconfig.Load()
	if err != nil {
		return err
	}
	path, err := appconfig.ConfigPath()
	if err != nil {
		return err
	}

	status := configurationStatus{
		Status:        "valid",
		SchemaVersion: cfg.SchemaVersion,
		Path:          path,
		Codex: codexConfigurationStatus{
			Command:      cfg.Codex.Command,
			DefaultModel: cfg.Codex.Model,
		},
		WeClaw: weClawConfigurationStatus{
			Reply: replyConfigurationStatus{
				Progress:       cfg.WeClaw.Reply.Progress.Enabled,
				Visual:         cfg.WeClaw.Reply.Visual.Enabled,
				LongReplies:    cfg.WeClaw.Reply.Visual.LongReplies,
				Voice:          cfg.WeClaw.Reply.Voice.Enabled,
				VoiceProviders: len(cfg.WeClaw.Reply.Voice.Providers),
				BrowserMode:    "自动发现",
			},
			Features: featureStatus{
				LinkArchive: cfg.WeClaw.Features.LinkArchive.Enabled,
				Automations: len(cfg.WeClaw.Features.Automations),
			},
			Security: securityStatus{RemoteLock: cfg.WeClaw.Security.RemoteLockCode != ""},
			SendAPI: sendAPIStatus{
				Enabled: cfg.WeClaw.SendAPI.Enabled,
				Callers: len(cfg.WeClaw.SendAPI.Tokens),
			},
		},
	}
	if cfg.WeClaw.Reply.Visual.BrowserCommand != "" {
		status.WeClaw.Reply.BrowserMode = "指定路径"
	}
	for _, entry := range cfg.WeClaw.ProjectEntries {
		status.WeClaw.ProjectEntries = append(status.WeClaw.ProjectEntries, projectEntryStatus{
			ID: entry.ID, Name: entry.Name,
			ServiceCheck: entry.ServiceName != "", HealthCheck: entry.HealthURL != "",
		})
	}

	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(status)
}
