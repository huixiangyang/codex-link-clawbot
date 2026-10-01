package cli

import (
	"fmt"
	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/statefile"
	"github.com/spf13/cobra"
)

func init() {
	command := &cobra.Command{Use: "binding", Short: "查看或选择唯一启用的微信绑定"}
	command.AddCommand(&cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		root, err := statefile.DefaultRoot()
		if err != nil {
			return err
		}
		accounts, err := ilink.LoadAllCredentials()
		if err != nil {
			return err
		}
		active, _ := ilink.ActiveBinding(root, accounts)
		for _, account := range accounts {
			status := "未启用"
			if active != nil && active.ILinkBotID == account.ILinkBotID {
				status = "已选择"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", account.ILinkBotID, status)
		}
		return nil
	}})
	command.AddCommand(&cobra.Command{Use: "select <bot-id>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		root, err := statefile.DefaultRoot()
		if err != nil {
			return err
		}
		lease, err := statefile.Acquire(root, statefile.LeaseMigration)
		if err != nil {
			return fmt.Errorf("请先停止服务再切换绑定: %w", err)
		}
		defer lease.Close()
		accounts, err := ilink.LoadAllCredentials()
		if err != nil {
			return err
		}
		if err := ilink.SelectBinding(root, args[0], accounts); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "绑定已选择，下次启动时生效。")
		return nil
	}})
	rootCmd.AddCommand(command)
}
