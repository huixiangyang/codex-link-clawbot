package cli

import (
	"encoding/json"
	"fmt"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/statefile"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
	"github.com/spf13/cobra"
)

func init() {
	var root, owner string
	var limit int
	command := &cobra.Command{Use: "artifacts", Short: "查看、校验和清理产物", PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if root == "" {
			var err error
			root, err = storage.DefaultRoot()
			if err != nil {
				return err
			}
		}
		return storage.CheckReady(root)
	}}
	command.PersistentFlags().StringVar(&root, "root", "", "状态根目录")
	command.PersistentFlags().StringVar(&owner, "owner", "", "按绑定者筛选，留空显示全部")
	list := &cobra.Command{Use: "list", Short: "按生成时间倒序列出有效产物", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		items, err := request.Catalogue(root, owner, limit)
		if err != nil {
			return err
		}
		encoder := json.NewEncoder(cmd.OutOrStdout())
		encoder.SetIndent("", "  ")
		return encoder.Encode(items)
	}}
	list.Flags().IntVar(&limit, "limit", 100, "最多返回的文件数量（1-8000）")
	verify := &cobra.Command{Use: "verify", Short: "校验保留期内的产物大小和 SHA-256", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		count, err := request.VerifyArtifacts(root, owner)
		if err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "已校验 %d 个产物。\n", count)
		return nil
	}}
	prune := &cobra.Command{Use: "prune", Short: "停服后按保留期清理，不提前删除有效结果", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if owner != "" {
			return fmt.Errorf("保留期清理作用于整个实例，请移除 --owner")
		}
		lease, err := statefile.Acquire(root, statefile.LeaseMigration)
		if err != nil {
			return err
		}
		defer lease.Close()
		store, err := request.NewStore(root)
		if err != nil {
			return err
		}
		if err := store.CleanupExpired(); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "已清理到期输入、结果和历史记录。")
		return nil
	}}
	command.AddCommand(list, verify, prune)
	rootCmd.AddCommand(command)
}
