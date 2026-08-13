package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags.
var Version = "dev"

var rootCmd = &cobra.Command{
	Use:     "codex-link-clawbot",
	Short:   "WeChat to Codex bridge",
	Long:    "codex-link-clawbot connects a bound WeChat account to a local Codex App Server.",
	Version: Version,
	RunE:    runStart, // default command is start
}

// Execute runs the root command.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
