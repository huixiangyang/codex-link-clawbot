package cmd

import (
	"encoding/json"
	"os"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(statusCmd)
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print structured runtime health",
	RunE: func(cmd *cobra.Command, args []string) error {
		controlSocket, err := defaultManagementSocketPath()
		if err != nil {
			return err
		}
		snapshot, err := fetchHealth(cmd.Context(), controlSocket)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(snapshot)
	},
}
