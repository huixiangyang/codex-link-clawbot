package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var (
	stopService string
	stopTimeout time.Duration
)

func init() {
	stopCmd.Flags().StringVar(&stopService, "service", defaultServiceName, "systemd user service name")
	stopCmd.Flags().DurationVar(&stopTimeout, "timeout", 10*time.Minute, "maximum drain wait")
	rootCmd.AddCommand(stopCmd)
}

var stopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Drain and stop the systemd user service",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		controlSocket, err := defaultManagementSocketPath()
		if err != nil {
			return err
		}
		if _, err := requestAdmin(ctx, controlSocket, "drain"); err != nil {
			return fmt.Errorf("request service drain: %w", err)
		}
		if _, err := waitForDrain(ctx, controlSocket, stopTimeout); err != nil {
			_, _ = requestAdmin(ctx, controlSocket, "resume")
			return err
		}
		if err := runSystemctl(ctx, stopService, "stop"); err != nil {
			_, _ = requestAdmin(ctx, controlSocket, "resume")
			return err
		}
		fmt.Println("WeClaw drained and stopped.")
		return nil
	},
}
