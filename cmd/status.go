package cmd

import (
	"encoding/json"
	"os"

	"github.com/spf13/cobra"
)

var statusAPI string

func init() {
	statusCmd.Flags().StringVar(&statusAPI, "api", defaultAPIBaseURL, "loopback runtime API origin")
	rootCmd.AddCommand(statusCmd)
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Print structured runtime health",
	RunE: func(cmd *cobra.Command, args []string) error {
		snapshot, err := fetchHealth(cmd.Context(), statusAPI)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(snapshot)
	},
}
