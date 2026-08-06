package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/huixiangyang/weclaw/internal/statefile"
	"github.com/huixiangyang/weclaw/internal/workflow"
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

var migrationIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

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
	for _, legacyName := range []string{"task-history.json", "weclaw.pid"} {
		path := filepath.Join(root, legacyName)
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove legacy %s: %w", legacyName, err)
		}
	}
	ownerIDs, err := migratedOwnerIDs(accountsPath)
	if err != nil {
		return fmt.Errorf("resolve workflow owners: %w", err)
	}
	if err := migrateProjectWorkflows(filepath.Join(root, "config.json"), filepath.Join(root, "workflows.json"), ownerIDs); err != nil {
		return fmt.Errorf("migrate project workflows: %w", err)
	}
	if err := migrateConfigurationV2(filepath.Join(root, "config.json")); err != nil {
		return fmt.Errorf("migrate config: %w", err)
	}
	if err := migrateControlState(filepath.Join(root, "control-state.json")); err != nil {
		return fmt.Errorf("migrate control state: %w", err)
	}
	return syncDirectoryPath(root)
}

// migrateControlState 丢弃旧版短期菜单和回执；Codex 线程、WeClaw 请求队列与提示词模板不依赖这些临时选择。
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
	case 5:
		return os.Chmod(path, 0o600)
	case 1, 2, 3, 4:
		return writePrivateJSONAtomic(path, struct {
			Version  int                        `json:"version"`
			Owners   map[string]json.RawMessage `json:"owners"`
			Receipts map[string]json.RawMessage `json:"receipts"`
		}{Version: 5, Owners: map[string]json.RawMessage{}, Receipts: map[string]json.RawMessage{}})
	default:
		return fmt.Errorf("unsupported control state version %d", state.Version)
	}
}

type legacyQuickTaskConfig struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Prompt string `json:"prompt"`
}

func migratedOwnerIDs(accountsPath string) ([]string, error) {
	entries, err := os.ReadDir(accountsPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") || strings.HasSuffix(entry.Name(), ".sync.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(accountsPath, entry.Name()))
		if err != nil {
			return nil, err
		}
		var credential currentCredentialState
		if err := decodeStrictJSONBytes(data, &credential); err != nil {
			return nil, err
		}
		if err := validateCredentialState(credential.Version, credential.BotToken, credential.ILinkBotID, credential.BaseURL, credential.ILinkUserID); err != nil {
			return nil, err
		}
		seen[credential.ILinkUserID] = true
	}
	owners := make([]string, 0, len(seen))
	for ownerID := range seen {
		owners = append(owners, ownerID)
	}
	sort.Strings(owners)
	return owners, nil
}

func migrateProjectWorkflows(configPath, workflowPath string, ownerIDs []string) error {
	info, err := os.Lstat(configPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("config must be a regular file")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := decodeStrictJSONBytes(data, &fields); err != nil {
		return err
	}
	projectsRaw, exists := fields["projects"]
	if !exists {
		return nil
	}
	var projects []map[string]json.RawMessage
	if err := decodeStrictJSONBytes(projectsRaw, &projects); err != nil {
		return fmt.Errorf("decode projects: %w", err)
	}
	projectIDs := make([]string, 0, len(projects))
	projectTasks := make(map[string][]legacyQuickTaskConfig)
	hadQuickTaskField := false
	hasQuickTasks := false
	for index, projectFields := range projects {
		var projectID string
		if rawID, ok := projectFields["id"]; !ok || json.Unmarshal(rawID, &projectID) != nil || !migrationIDPattern.MatchString(projectID) {
			return fmt.Errorf("projects[%d].id is invalid", index)
		}
		projectIDs = append(projectIDs, projectID)
		rawTasks, ok := projectFields["quick_tasks"]
		if !ok {
			continue
		}
		hadQuickTaskField = true
		var tasks []legacyQuickTaskConfig
		if err := decodeStrictJSONBytes(rawTasks, &tasks); err != nil {
			return fmt.Errorf("decode projects[%d].quick_tasks: %w", index, err)
		}
		seenIDs := make(map[string]bool, len(tasks))
		for taskIndex, task := range tasks {
			if !migrationIDPattern.MatchString(task.ID) || seenIDs[task.ID] {
				return fmt.Errorf("projects[%d].quick_tasks[%d].id is invalid or duplicated", index, taskIndex)
			}
			seenIDs[task.ID] = true
		}
		if len(tasks) > 0 {
			hasQuickTasks = true
			projectTasks[projectID] = tasks
		}
		delete(projectFields, "quick_tasks")
	}
	if !hadQuickTaskField {
		return nil
	}
	if hasQuickTasks {
		if len(ownerIDs) == 0 {
			return fmt.Errorf("cannot assign configured quick tasks without a bound owner")
		}
		store, err := workflow.NewStore(workflowPath, projectIDs)
		if err != nil {
			return err
		}
		for _, ownerID := range ownerIDs {
			for _, projectID := range projectIDs {
				for _, task := range projectTasks[projectID] {
					definition := workflow.Definition{
						ID: workflow.StableImportID(projectID, task.ID), ProjectID: projectID,
						Name: strings.TrimSpace(task.Name), PromptTemplate: strings.TrimSpace(task.Prompt),
						Slots: []workflow.Slot{}, CreatedAt: 1, UpdatedAt: 1,
					}
					if _, err := store.Import(ownerID, definition); err != nil {
						return err
					}
				}
			}
		}
	}
	encodedProjects, err := json.Marshal(projects)
	if err != nil {
		return err
	}
	fields["projects"] = encodedProjects
	return writePrivateJSONAtomic(configPath, fields)
}

// migrateConfigurationV2 将扁平旧配置一次性重组为 Codex 与 WeClaw 两层；运行时不读取旧结构。
func migrateConfigurationV2(path string) error {
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
	if rawVersion, exists := fields["schema_version"]; exists {
		var version int
		if err := json.Unmarshal(rawVersion, &version); err != nil || version != 2 {
			return fmt.Errorf("unsupported configuration schema version")
		}
		return os.Chmod(path, 0o600)
	}
	allowed := map[string]bool{
		"save_dir": true, "send_api": true, "progress": true, "projects": true,
		"automations": true, "codex": true, "visual": true, "security": true, "voice": true,
	}
	for name := range fields {
		if !allowed[name] {
			return fmt.Errorf("unknown field %q", name)
		}
	}

	reply := make(map[string]json.RawMessage)
	for _, name := range []string{"progress", "visual", "voice"} {
		if value, exists := fields[name]; exists {
			reply[name] = value
		}
	}
	features := make(map[string]json.RawMessage)
	if value, exists := fields["automations"]; exists {
		features["automations"] = value
	}
	linkArchive := struct {
		Enabled   bool   `json:"enabled"`
		Directory string `json:"directory,omitempty"`
	}{}
	if value, exists := fields["save_dir"]; exists {
		if err := json.Unmarshal(value, &linkArchive.Directory); err != nil {
			return fmt.Errorf("decode save_dir: %w", err)
		}
		linkArchive.Enabled = strings.TrimSpace(linkArchive.Directory) != ""
	}
	linkArchiveJSON, err := json.Marshal(linkArchive)
	if err != nil {
		return err
	}
	features["link_archive"] = linkArchiveJSON

	weclaw := make(map[string]json.RawMessage)
	if value, exists := fields["projects"]; exists {
		weclaw["project_entries"] = value
	}
	if value, exists := fields["security"]; exists {
		weclaw["security"] = value
	}
	if value, exists := fields["send_api"]; exists {
		weclaw["send_api"] = value
	} else {
		weclaw["send_api"] = json.RawMessage(`{"enabled":false}`)
	}
	replyJSON, err := json.Marshal(reply)
	if err != nil {
		return err
	}
	featuresJSON, err := json.Marshal(features)
	if err != nil {
		return err
	}
	weclaw["reply"] = replyJSON
	weclaw["features"] = featuresJSON
	weclawJSON, err := json.Marshal(weclaw)
	if err != nil {
		return err
	}

	current := map[string]json.RawMessage{
		"schema_version": json.RawMessage(`2`),
		"weclaw":         weclawJSON,
	}
	if value, exists := fields["codex"]; exists {
		current["codex"] = value
	}
	return writePrivateJSONAtomic(path, current)
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
