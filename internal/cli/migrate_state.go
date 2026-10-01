package cli

import (
	"fmt"

	"github.com/huixiangyang/codex-link-clawbot/internal/app/migration"
	"github.com/spf13/cobra"
)

func init() {
	var root string
	command := &cobra.Command{Use: "migrate-state", Short: "停服后将最后一代 JSON 状态离线迁入 SQLite", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		backup, err := migration.Run(root)
		if backup != "" {
			fmt.Fprintln(cmd.OutOrStdout(), "旧状态备份：", backup)
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "SQLite 状态已就绪，没有连接微信或执行 Codex 请求。")
		return nil
	}}
	command.Flags().StringVar(&root, "root", "", "状态根目录的绝对路径")
	_ = command.MarkFlagRequired("root")
	rootCmd.AddCommand(command)
}
