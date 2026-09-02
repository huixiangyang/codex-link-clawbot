package cli

import (
	"encoding/json"
	"fmt"
	"os"

	appconfig "github.com/huixiangyang/codex-link-clawbot/internal/config"
	"github.com/huixiangyang/codex-link-clawbot/internal/management"
	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
	"github.com/spf13/cobra"
)

type consoleAccess struct {
	URL       string `json:"url"`
	Token     string `json:"token"`
	TokenPath string `json:"token_path"`
}

func init() {
	rootCmd.AddCommand(consoleCmd)
}

var consoleCmd = &cobra.Command{
	Use:   "console",
	Short: "Print management console access details",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		stateRoot, err := statefile.DefaultRoot()
		if err != nil {
			return fmt.Errorf("resolve state root: %w", err)
		}
		token, err := management.LoadConsoleToken(stateRoot)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("management token does not exist; start codex-link-clawbot first")
			}
			return err
		}
		cfg, err := appconfig.Load()
		if err != nil {
			return err
		}
		url := cfg.Clawbot.Management.PublicURL
		if url == "" {
			url = "http://" + cfg.Clawbot.Management.Listen
		}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(consoleAccess{URL: url, Token: token, TokenPath: management.ConsoleTokenPath(stateRoot)})
	},
}
