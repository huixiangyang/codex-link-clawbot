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
		return migrateStateV25(migrationStateRoot)
	},
}

func migrateStateV25(root string) error {
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
	accountsPath := filepath.Join(root, "accounts")
	entries, err := os.ReadDir(accountsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("list account state: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sync.json") {
			continue
		}
		path := filepath.Join(accountsPath, entry.Name())
		if err := migrateSyncFile(path); err != nil {
			return fmt.Errorf("migrate %s: %w", entry.Name(), err)
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
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return writeAtomicFile(path, append(data, '\n'), 0o600)
}
