package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var (
	restartService string
	restartTimeout time.Duration
)

func init() {
	restartCmd.Flags().StringVar(&restartService, "service", defaultServiceName, "systemd user service name")
	restartCmd.Flags().DurationVar(&restartTimeout, "timeout", 10*time.Minute, "maximum drain and readiness wait")
	rootCmd.AddCommand(restartCmd)
}

var restartCmd = &cobra.Command{
	Use:   "restart",
	Short: "Drain and restart the systemd user service",
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx := cmd.Context()
		controlSocket, err := defaultManagementSocketPath()
		if err != nil {
			return err
		}
		draining, err := requestAdmin(ctx, controlSocket, "drain")
		if err != nil {
			return fmt.Errorf("request service drain: %w", err)
		}
		if _, err := waitForDrain(ctx, controlSocket, restartTimeout); err != nil {
			_, _ = requestAdmin(ctx, controlSocket, "resume")
			return err
		}
		if err := runSystemctl(ctx, restartService, "restart"); err != nil {
			_, _ = requestAdmin(ctx, controlSocket, "resume")
			return err
		}
		if _, err := waitForReady(ctx, controlSocket, draining.Version, restartTimeout); err != nil {
			return err
		}
		fmt.Printf("WeClaw %s restarted and ready.\n", draining.Version)
		return nil
	},
}
