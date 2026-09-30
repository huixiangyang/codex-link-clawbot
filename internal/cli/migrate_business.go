package cli

import (
	"fmt"
	"github.com/huixiangyang/codex-link-clawbot/internal/businessmigration"
	"github.com/spf13/cobra"
)

func init() {
	var root, workspace string
	command := &cobra.Command{Use: "migrate-business", Short: "离线迁移会话目标、请求和交付结果（自动备份）", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		backup, err := businessmigration.Run(root, workspace)
		if backup != "" {
			fmt.Fprintln(cmd.OutOrStdout(), "原始数据备份：", backup)
		}
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "业务状态已就绪。迁移没有连接微信或执行 Codex 请求。")
		return nil
	}}
	command.Flags().StringVar(&root, "root", "", "状态根目录的绝对路径")
	command.Flags().StringVar(&workspace, "default-workspace", "", "配置中第一个受信任工作空间的 ID")
	_ = command.MarkFlagRequired("root")
	_ = command.MarkFlagRequired("default-workspace")
	rootCmd.AddCommand(command)
}
