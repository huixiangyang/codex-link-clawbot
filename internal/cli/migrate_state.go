package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
	"github.com/spf13/cobra"
)

type legacySyncState struct {
	GetUpdatesBuf string `json:"get_updates_buf"`
}

type currentSyncState struct {
	Version       int      `json:"version"`
	GetUpdatesBuf string   `json:"get_updates_buf"`
	PendingCursor string   `json:"pending_cursor,omitempty"`
	Consumed      []string `json:"consumed,omitempty"`
}

type legacyCredentialState struct {
	BotToken    string `json:"bot_token"`
	ILinkBotID  string `json:"ilink_bot_id"`
	BaseURL     string `json:"baseurl"`
	ILinkUserID string `json:"ilink_user_id"`
}

type currentCredentialState struct {
	Version     int    `json:"version"`
	BotToken    string `json:"bot_token"`
	ILinkBotID  string `json:"ilink_bot_id"`
	BaseURL     string `json:"baseurl"`
	ILinkUserID string `json:"ilink_user_id"`
}

var migrationStateRoot string

func init() {
	migrateStateCmd.Flags().StringVar(&migrationStateRoot, "root", "", "absolute codex-link-clawbot state root")
	_ = migrateStateCmd.MarkFlagRequired("root")
	rootCmd.AddCommand(migrateStateCmd)
}

var migrateStateCmd = &cobra.Command{
	Use:    "migrate-state",
	Short:  "Run the one-shot offline state migration",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return migrateState(migrationStateRoot)
	},
}

func migrateState(root string) error {
	root = filepath.Clean(strings.TrimSpace(root))
	if !filepath.IsAbs(root) || root == string(filepath.Separator) {
		return fmt.Errorf("state root must be a specific absolute path")
	}
	info, err := os.Lstat(root)
	if err != nil {
		return fmt.Errorf("inspect state root: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("state root must be a real directory")
	}
	lease, err := statefile.Acquire(root, statefile.LeaseMigration)
	if err != nil {
		return fmt.Errorf("acquire migration state lease: %w", err)
	}
	defer lease.Close()
	accountsPath := filepath.Join(root, "accounts")
	entries, err := os.ReadDir(accountsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("list account state: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		path := filepath.Join(accountsPath, entry.Name())
		var migrateErr error
		switch {
		case strings.HasSuffix(entry.Name(), ".sync.json"):
			migrateErr = migrateSyncFile(path)
		case strings.HasSuffix(entry.Name(), ".json"):
			migrateErr = migrateCredentialFile(path)
		default:
			continue
		}
		if migrateErr != nil {
			return fmt.Errorf("migrate %s: %w", entry.Name(), migrateErr)
		}
	}
	for _, legacyName := range []string{"task-history.json", "codex-link-clawbot.pid"} {
		path := filepath.Join(root, legacyName)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove legacy %s: %w", legacyName, err)
		}
	}
	if err := removeRetiredPromptTemplates(filepath.Join(root, "workflows.json")); err != nil {
		return fmt.Errorf("remove prompt templates: %w", err)
	}
	if err := validateConfigurationV6(filepath.Join(root, "config.json")); err != nil {
		return fmt.Errorf("validate config: %w", err)
	}
	if err := migrateDeliveryLibrary(root); err != nil {
		return fmt.Errorf("migrate delivery library: %w", err)
	}
	if err := removeRetiredProjectMonitoringState(root); err != nil {
		return fmt.Errorf("remove retired project monitoring state: %w", err)
	}
	if err := removeRetiredProjectWatchNotices(filepath.Join(root, "pending-notices.json")); err != nil {
		return fmt.Errorf("remove retired project watch notices: %w", err)
	}
	if err := migrateControlState(filepath.Join(root, "control-state.json")); err != nil {
		return fmt.Errorf("migrate control state: %w", err)
	}
	return syncDirectoryPath(root)
}

// migrateControlState 丢弃旧版短期菜单和回执；v14 为目标线程加入原生关系图并重排管理编号。
func migrateControlState(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("control state must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var state struct {
		Version  int                        `json:"version"`
		Owners   map[string]json.RawMessage `json:"owners"`
		Receipts map[string]json.RawMessage `json:"receipts"`
	}
	if err := decodeStrictJSONBytes(data, &state); err != nil {
		return err
	}
	if state.Version <= 0 || state.Owners == nil || state.Receipts == nil {
		return fmt.Errorf("control state schema is invalid")
	}
	switch state.Version {
	case 14:
		return os.Chmod(path, 0o600)
	case 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13:
		return writePrivateJSONAtomic(path, struct {
			Version  int                        `json:"version"`
			Owners   map[string]json.RawMessage `json:"owners"`
			Receipts map[string]json.RawMessage `json:"receipts"`
		}{Version: 14, Owners: map[string]json.RawMessage{}, Receipts: map[string]json.RawMessage{}})
	default:
		return fmt.Errorf("unsupported control state version %d", state.Version)
	}
}

// removeRetiredPromptTemplates 直接销毁当前命名空间中已下线的模板状态。
// 历史配置不在这里改写，而是由严格 v6 校验直接拒绝。
func removeRetiredPromptTemplates(workflowPath string) error {
	workflowInfo, workflowErr := os.Lstat(workflowPath)
	if workflowErr == nil {
		if !workflowInfo.Mode().IsRegular() || workflowInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("workflow state must be a regular file")
		}
		if err := os.Remove(workflowPath); err != nil {
			return err
		}
	} else if !errors.Is(workflowErr, os.ErrNotExist) {
		return workflowErr
	}
	return nil
}

// validateConfigurationV6 拒绝定时重复进度字段；阶段五只保留真实阶段更新节奏。
func validateConfigurationV6(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("config must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := decodeStrictJSONBytes(data, &fields); err != nil {
		return err
	}
	if fields == nil {
		return fmt.Errorf("config must be a JSON object")
	}
	allowed := map[string]bool{"schema_version": true, "codex": true, "codex-link-clawbot": true}
	for name := range fields {
		if !allowed[name] {
			return fmt.Errorf("unknown field %q", name)
		}
	}
	rawVersion, exists := fields["schema_version"]
	if !exists {
		return fmt.Errorf("schema_version is required")
	}
	var version int
	if err := json.Unmarshal(rawVersion, &version); err != nil || version != 6 {
		return fmt.Errorf("unsupported configuration schema version")
	}
	if _, exists := fields["codex"]; !exists {
		return fmt.Errorf("codex is required")
	}
	if _, exists := fields["codex-link-clawbot"]; !exists {
		return fmt.Errorf("codex-link-clawbot is required")
	}
	var clawbot struct {
		ProjectEntries json.RawMessage `json:"project_entries"`
		Reply          json.RawMessage `json:"reply"`
		Security       json.RawMessage `json:"security"`
	}
	if err := decodeStrictJSONBytes(fields["codex-link-clawbot"], &clawbot); err != nil {
		return err
	}
	if len(clawbot.Reply) > 0 {
		var reply struct {
			Progress json.RawMessage `json:"progress"`
			Visual   json.RawMessage `json:"visual"`
			Voice    json.RawMessage `json:"voice"`
		}
		if err := decodeStrictJSONBytes(clawbot.Reply, &reply); err != nil {
			return err
		}
		if len(reply.Progress) > 0 {
			var progress struct {
				Enabled                  bool `json:"enabled"`
				TypingIntervalSeconds    int  `json:"typing_interval_seconds"`
				FirstMessageDelaySeconds int  `json:"first_message_delay_seconds"`
			}
			if err := decodeStrictJSONBytes(reply.Progress, &progress); err != nil {
				return err
			}
		}
	}
	return os.Chmod(path, 0o600)
}

// migrateDeliveryLibrary 破坏性升级交付箱：v3 要求每个文件都有任务、线程与摘要校验来源。
// 旧记录无法可靠补齐这些事实，因此销毁旧私有副本，不保留运行时兼容分支。
func migrateDeliveryLibrary(root string) error {
	root = filepath.Clean(root)
	path := filepath.Join(root, "library.json")
	archivePath := filepath.Join(root, "deliveries")
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return resetDeliveryLibrary(path, archivePath)
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("delivery library must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return err
	}
	if header.Version == 3 {
		return os.Chmod(path, 0o600)
	}
	if header.Version != 1 && header.Version != 2 {
		return fmt.Errorf("unsupported delivery library version %d", header.Version)
	}
	return resetDeliveryLibrary(path, archivePath)
}

func resetDeliveryLibrary(path, archivePath string) error {
	if info, err := os.Lstat(archivePath); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("delivery archive must be a real directory")
		}
		if err := os.RemoveAll(archivePath); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := writePrivateJSONAtomic(path, struct {
		Version int                          `json:"version"`
		Owners  map[string][]json.RawMessage `json:"owners"`
	}{Version: 3, Owners: map[string][]json.RawMessage{}}); err != nil {
		return err
	}
	return syncDirectoryPath(filepath.Dir(path))
}

// removeRetiredProjectMonitoringState 销毁所有历史监控游标，不迁入其他业务状态。
func removeRetiredProjectMonitoringState(root string) error {
	for _, name := range []string{"automation-state.json", "project-watch-state.json", "scheduled-reports-state.json"} {
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("retired project monitoring state %s must be a regular file", name)
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return syncDirectoryPath(root)
}

type migratedPendingNotice struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	DedupKey    string `json:"dedup_key"`
	ReferenceID string `json:"reference_id,omitempty"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	CreatedAt   int64  `json:"created_at"`
	ExpiresAt   int64  `json:"expires_at"`
}

// removeRetiredProjectWatchNotices 只删除已下线项目关注产生的待阅消息。
func removeRetiredProjectWatchNotices(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("pending notice state must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var state struct {
		Version int                                `json:"version"`
		Owners  map[string][]migratedPendingNotice `json:"owners"`
	}
	if err := decodeStrictJSONBytes(data, &state); err != nil {
		return err
	}
	if state.Version != 1 || state.Owners == nil {
		return fmt.Errorf("pending notice state schema is invalid")
	}
	changed := false
	for ownerID, notices := range state.Owners {
		kept := notices[:0]
		for _, notice := range notices {
			switch notice.Kind {
			case "project_watch":
				changed = true
			case "deployment", "task_recovery":
				kept = append(kept, notice)
			default:
				return fmt.Errorf("pending notice kind %q is unsupported", notice.Kind)
			}
		}
		if len(kept) == 0 {
			delete(state.Owners, ownerID)
		} else {
			state.Owners[ownerID] = kept
		}
	}
	if !changed {
		return os.Chmod(path, 0o600)
	}
	return writePrivateJSONAtomic(path, state)
}

func migrateCredentialFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("credentials must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var header map[string]json.RawMessage
	if err := decodeStrictJSONBytes(data, &header); err != nil {
		return err
	}
	if _, exists := header["version"]; exists {
		var current currentCredentialState
		if err := decodeStrictJSONBytes(data, &current); err != nil {
			return err
		}
		if err := validateCredentialState(current.Version, current.BotToken, current.ILinkBotID, current.BaseURL, current.ILinkUserID); err != nil {
			return err
		}
		return os.Chmod(path, 0o600)
	}
	var legacy legacyCredentialState
	if err := decodeStrictJSONBytes(data, &legacy); err != nil {
		return fmt.Errorf("unsupported legacy credentials schema: %w", err)
	}
	if err := validateCredentialState(1, legacy.BotToken, legacy.ILinkBotID, legacy.BaseURL, legacy.ILinkUserID); err != nil {
		return err
	}
	return writePrivateJSONAtomic(path, currentCredentialState{
		Version: 1, BotToken: legacy.BotToken, ILinkBotID: legacy.ILinkBotID,
		BaseURL: legacy.BaseURL, ILinkUserID: legacy.ILinkUserID,
	})
}

func validateCredentialState(version int, token, botID, baseURL, userID string) error {
	if version != 1 || strings.TrimSpace(token) == "" || strings.TrimSpace(botID) == "" || strings.TrimSpace(baseURL) == "" || strings.TrimSpace(userID) == "" {
		return fmt.Errorf("invalid v1 credentials")
	}
	for _, value := range []string{token, botID, baseURL, userID} {
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("invalid multiline credentials")
		}
	}
	return nil
}

func migrateSyncFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("sync state must be a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var header map[string]json.RawMessage
	if err := decodeStrictJSONBytes(data, &header); err != nil {
		return err
	}
	var current currentSyncState
	if _, exists := header["version"]; exists {
		if err := decodeStrictJSONBytes(data, &current); err != nil {
			return err
		}
		if err := validateCurrentSyncState(current); err != nil {
			return err
		}
		return os.Chmod(path, 0o600)
	}
	var legacy legacySyncState
	if err := decodeStrictJSONBytes(data, &legacy); err != nil {
		return fmt.Errorf("unsupported legacy sync schema: %w", err)
	}
	return writePrivateJSONAtomic(path, currentSyncState{Version: 1, GetUpdatesBuf: legacy.GetUpdatesBuf})
}

func validateCurrentSyncState(state currentSyncState) error {
	if state.Version != 1 || state.PendingCursor == "" && len(state.Consumed) > 0 || len(state.Consumed) > 512 {
		return fmt.Errorf("invalid v1 sync state")
	}
	seen := make(map[string]bool, len(state.Consumed))
	for _, key := range state.Consumed {
		if strings.TrimSpace(key) == "" || seen[key] {
			return fmt.Errorf("invalid v1 sync receipt")
		}
		seen[key] = true
	}
	return nil
}

func decodeStrictJSONBytes(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}

func writePrivateJSONAtomic(path string, value any) error {
	return statefile.WriteJSON(path, value, statefile.Options{})
}
