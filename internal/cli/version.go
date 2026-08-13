package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"
)

type versionOutput struct {
	Version string `json:"version"`
	GOOS    string `json:"goos"`
	GOARCH  string `json:"goarch"`
}

var versionJSON bool

func init() {
	versionCmd.Flags().BoolVar(&versionJSON, "json", false, "print machine-readable version metadata")
	rootCmd.AddCommand(versionCmd)
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the current version",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if versionJSON {
			return json.NewEncoder(os.Stdout).Encode(versionOutput{Version: Version, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH})
		}
		fmt.Printf("codex-link-clawbot %s (%s/%s)\n", Version, runtime.GOOS, runtime.GOARCH)
		return nil
	},
}
