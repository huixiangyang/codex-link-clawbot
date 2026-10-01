package cli

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/huixiangyang/codex-link-clawbot/internal/app/config"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/statefile"
	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
	"github.com/spf13/cobra"
)

// 配置写入要求停服，避免运行进程继续使用已经失效的配置快照。
func editConfiguration(change func(*config.Config) error) error {
	root, err := storage.DefaultRoot()
	if err != nil {
		return err
	}
	if err := storage.CheckReady(root); err != nil {
		return err
	}
	lease, err := statefile.Acquire(root, statefile.LeaseMigration)
	if err != nil {
		return err
	}
	defer lease.Close()
	cfg, err := config.LoadRoot(root)
	if err != nil {
		return err
	}
	if err := change(cfg); err != nil {
		return err
	}
	return config.SaveRoot(root, cfg)
}

func init() {
	set := &cobra.Command{Use: "set <key> <value>", Short: "停服后修改一个标量配置，不在输出中回显值", Args: cobra.ExactArgs(2), RunE: func(cmd *cobra.Command, args []string) error {
		if err := editConfiguration(func(cfg *config.Config) error { return config.SetValue(cfg, args[0], args[1]) }); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "配置已保存到 SQLite，下次启动生效。")
		return nil
	}}
	apply := &cobra.Command{Use: "apply", Short: "从标准输入接收完整配置协议并事务提交到 SQLite", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		data, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), (4<<20)+1))
		if err != nil {
			return err
		}
		if len(data) > 4<<20 {
			return fmt.Errorf("configuration exceeds limit")
		}
		cfg := config.DefaultConfig()
		if err := json.Unmarshal(data, cfg); err != nil {
			return err
		}
		if err := editConfiguration(func(current *config.Config) error { *current = *cfg; return nil }); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "配置已保存到 SQLite，下次启动生效。")
		return nil
	}}
	workspace := &cobra.Command{Use: "workspace", Short: "管理受信任工作空间"}
	workspace.AddCommand(&cobra.Command{Use: "set <id> <name> <absolute-root>", Short: "新增或更新工作空间", Args: cobra.ExactArgs(3), RunE: func(cmd *cobra.Command, args []string) error {
		return editConfiguration(func(cfg *config.Config) error {
			next := config.ProjectConfig{ID: args[0], Name: args[1], Root: args[2]}
			for i, p := range cfg.Clawbot.ProjectEntries {
				if p.ID == next.ID {
					cfg.Clawbot.ProjectEntries[i] = next
					return nil
				}
			}
			cfg.Clawbot.ProjectEntries = append(cfg.Clawbot.ProjectEntries, next)
			return nil
		})
	}})
	workspace.AddCommand(&cobra.Command{Use: "remove <id>", Short: "移除工作空间，至少保留一个", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		return editConfiguration(func(cfg *config.Config) error {
			for i, p := range cfg.Clawbot.ProjectEntries {
				if p.ID == args[0] {
					cfg.Clawbot.ProjectEntries = append(cfg.Clawbot.ProjectEntries[:i], cfg.Clawbot.ProjectEntries[i+1:]...)
					return nil
				}
			}
			return fmt.Errorf("工作空间不存在")
		})
	}})
	configCmd.AddCommand(set, apply, workspace)
}
