package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/huixiangyang/weclaw/statefile"
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
	migrateStateCmd.Flags().StringVar(&migrationStateRoot, "root", "", "absolute WeClaw state root")
	_ = migrateStateCmd.MarkFlagRequired("root")
	rootCmd.AddCommand(migrateStateCmd)
}

var migrateStateCmd = &cobra.Command{
	Use:    "migrate-state",
	Short:  "Run the one-shot offline state migration",
	Hidden: true,
	Args:   cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return migrateStateV26(migrationStateRoot)
	},
}

func migrateStateV26(root string) error {
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
	for _, legacyName := range []string{"task-history.json", "weclaw.pid"} {
		path := filepath.Join(root, legacyName)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove legacy %s: %w", legacyName, err)
		}
	}
	return syncDirectoryPath(root)
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
