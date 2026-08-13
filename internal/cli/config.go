package cli

import (
	"encoding/json"

	appconfig "github.com/huixiangyang/codex-link-clawbot/internal/config"
	"github.com/spf13/cobra"
)

type configurationStatus struct {
	Status        string                     `json:"status"`
	SchemaVersion int                        `json:"schema_version"`
	Path          string                     `json:"path"`
	Codex         codexConfigurationStatus   `json:"codex"`
	Clawbot       clawbotConfigurationStatus `json:"codex-link-clawbot"`
}

type codexConfigurationStatus struct {
	Command      string `json:"command"`
	DefaultModel string `json:"default_model,omitempty"`
}

type clawbotConfigurationStatus struct {
	ProjectEntries []projectEntryStatus     `json:"project_entries"`
	Reply          replyConfigurationStatus `json:"reply"`
	Security       securityStatus           `json:"security"`
}

type projectEntryStatus struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type replyConfigurationStatus struct {
	Progress       bool   `json:"progress"`
	Visual         bool   `json:"visual"`
	LongReplies    bool   `json:"long_replies"`
	Voice          bool   `json:"voice"`
	VoiceProviders int    `json:"voice_providers"`
	BrowserMode    string `json:"browser_mode"`
}

type securityStatus struct {
	RemoteLock bool `json:"remote_lock"`
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
		Clawbot: clawbotConfigurationStatus{
			Reply: replyConfigurationStatus{
				Progress:       cfg.Clawbot.Reply.Progress.Enabled,
				Visual:         cfg.Clawbot.Reply.Visual.Enabled,
				LongReplies:    cfg.Clawbot.Reply.Visual.LongReplies,
				Voice:          cfg.Clawbot.Reply.Voice.Enabled,
				VoiceProviders: len(cfg.Clawbot.Reply.Voice.Providers),
				BrowserMode:    "自动发现",
			},
			Security: securityStatus{RemoteLock: cfg.Clawbot.Security.RemoteLockCode != ""},
		},
	}
	if cfg.Clawbot.Reply.Visual.BrowserCommand != "" {
		status.Clawbot.Reply.BrowserMode = "指定路径"
	}
	for _, entry := range cfg.Clawbot.ProjectEntries {
		status.Clawbot.ProjectEntries = append(status.Clawbot.ProjectEntries, projectEntryStatus{
			ID: entry.ID, Name: entry.Name,
		})
	}

	encoder := json.NewEncoder(cmd.OutOrStdout())
	encoder.SetIndent("", "  ")
	return encoder.Encode(status)
}
