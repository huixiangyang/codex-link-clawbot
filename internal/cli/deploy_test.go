package cli

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/huixiangyang/weclaw/internal/statefile"
	"github.com/huixiangyang/weclaw/internal/workflow"
)

func TestMigrateStateV26ConvertsOnlyKnownLegacyState(t *testing.T) {
	root := t.TempDir()
	accounts := filepath.Join(root, "accounts")
	if err := os.MkdirAll(accounts, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := filepath.Join(accounts, "owner.sync.json")
	if err := os.WriteFile(legacy, []byte(`{"get_updates_buf":"cursor-1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	current := filepath.Join(accounts, "current.sync.json")
	if err := os.WriteFile(current, []byte(`{"version":1,"get_updates_buf":"cursor-2","pending_cursor":"pending","consumed":["message:1"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"task-history.json", "weclaw.pid"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("legacy"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	credentialsPath := filepath.Join(accounts, "bot.json")
	if err := os.WriteFile(credentialsPath, []byte(`{"bot_token":"secret","ilink_bot_id":"bot","baseurl":"https://ilinkai.weixin.qq.com","ilink_user_id":"owner"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateState(root); err != nil {
		t.Fatalf("migrateState() error = %v", err)
	}
	data, err := os.ReadFile(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\n  \"version\": 1,\n  \"get_updates_buf\": \"cursor-1\"\n}\n" {
		t.Fatalf("migrated sync = %q", data)
	}
	if info, err := os.Stat(legacy); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("migrated sync mode = %v, %v", info.Mode().Perm(), err)
	}
	credentialsData, err := os.ReadFile(credentialsPath)
	if err != nil {
		t.Fatal(err)
	}
	var credentials currentCredentialState
	if err := decodeStrictJSONBytes(credentialsData, &credentials); err != nil || credentials.Version != 1 || credentials.BotToken != "secret" {
		t.Fatalf("migrated credentials = %#v, err=%v", credentials, err)
	}
	for _, name := range []string{"task-history.json", "weclaw.pid"} {
		if _, err := os.Stat(filepath.Join(root, name)); !os.IsNotExist(err) {
			t.Fatalf("legacy %s still exists: %v", name, err)
		}
	}
	if err := migrateState(root); err != nil {
		t.Fatalf("idempotent migrateState() error = %v", err)
	}
}

func TestMigrateStateV26RejectsUnknownSyncSchema(t *testing.T) {
	root := t.TempDir()
	accounts := filepath.Join(root, "accounts")
	if err := os.MkdirAll(accounts, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(accounts, "owner.sync.json")
	if err := os.WriteFile(path, []byte(`{"get_updates_buf":"cursor","unknown":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateState(root); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("migrateState() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "task-history.json")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

func TestMigrateStateV26RejectsRunningStateLease(t *testing.T) {
	root := t.TempDir()
	lease, err := statefile.Acquire(root, statefile.LeaseRuntime)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err := migrateState(root); statefile.ErrorCategory(err) != statefile.CategoryConflict {
		t.Fatalf("migration error = %v, category = %q", err, statefile.ErrorCategory(err))
	}
}

func TestMigrateStateV26DisablesLegacyUnauthenticatedAPI(t *testing.T) {
	root := t.TempDir()
	legacy := `{"api_addr":"127.0.0.1:18011","progress":{"enabled":true}}`
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateState(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "api_addr") || !strings.Contains(string(data), `"send_api"`) || !strings.Contains(string(data), `"enabled": false`) {
		t.Fatalf("migrated config=%s", data)
	}
}

func TestMigrateStateReplacesV1ControlStateWithEmptyV2(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "control-state.json")
	legacy := `{"version":1,"owners":{"owner":{"revision":"0123456789abcdef0123456789abcdef"}},"receipts":{"source":{"action_id":"session.new"}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := migrateState(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\n  \"version\": 2,\n  \"owners\": {},\n  \"receipts\": {}\n}\n" {
		t.Fatalf("migrated control state = %s", data)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("control state mode = %v, %v", info.Mode().Perm(), err)
	}
}

func TestMigrateStateRejectsUnknownControlStateVersion(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "control-state.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"owners":{},"receipts":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateState(root); err == nil || !strings.Contains(err.Error(), "unsupported control state version") {
		t.Fatalf("migrateState() error = %v", err)
	}
}

func TestMigrateStateRejectsUnknownControlStateField(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "control-state.json")
	if err := os.WriteFile(path, []byte(`{"version":2,"owners":{},"receipts":{},"legacy":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateState(root); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("migrateState() error = %v", err)
	}
}

func TestMigrateStateMovesConfiguredQuickTasksIntoOwnerWorkflows(t *testing.T) {
	root := t.TempDir()
	accounts := filepath.Join(root, "accounts")
	if err := os.MkdirAll(accounts, 0o700); err != nil {
		t.Fatal(err)
	}
	credential := `{"version":1,"bot_token":"secret","ilink_bot_id":"bot","baseurl":"https://ilinkai.weixin.qq.com","ilink_user_id":"owner-1"}`
	if err := os.WriteFile(filepath.Join(accounts, "bot.json"), []byte(credential), 0o600); err != nil {
		t.Fatal(err)
	}
	configData := `{"projects":[{"id":"weclaw","name":"WeClaw","root":"/srv/weclaw","quick_tasks":[{"id":"review","name":"审查改动","prompt":"审查当前改动"}]}]}`
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateState(root); err != nil {
		t.Fatal(err)
	}
	migratedConfig, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(migratedConfig), "quick_tasks") {
		t.Fatalf("legacy quick_tasks survived migration: %s", migratedConfig)
	}
	store, err := workflow.NewStore(filepath.Join(root, "workflows.json"), []string{"weclaw"})
	if err != nil {
		t.Fatal(err)
	}
	definitions := store.List("owner-1", "weclaw")
	if len(definitions) != 1 || definitions[0].Name != "审查改动" || definitions[0].PromptTemplate != "审查当前改动" || len(definitions[0].Slots) != 0 {
		t.Fatalf("migrated workflows = %#v", definitions)
	}
	if err := migrateState(root); err != nil {
		t.Fatalf("idempotent workflow migration error = %v", err)
	}
	reopened, err := workflow.NewStore(filepath.Join(root, "workflows.json"), []string{"weclaw"})
	if err != nil {
		t.Fatal(err)
	}
	if definitions := reopened.List("owner-1", "weclaw"); len(definitions) != 1 {
		t.Fatalf("workflow migration duplicated definitions: %#v", definitions)
	}
}

func TestMigrateStateRefusesQuickTasksWithoutBoundOwner(t *testing.T) {
	root := t.TempDir()
	configData := `{"projects":[{"id":"weclaw","name":"WeClaw","root":"/srv/weclaw","quick_tasks":[{"id":"review","name":"审查改动","prompt":"审查当前改动"}]}]}`
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateState(root); err == nil || !strings.Contains(err.Error(), "without a bound owner") {
		t.Fatalf("migration error = %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil || !strings.Contains(string(data), "quick_tasks") {
		t.Fatalf("failed migration removed source tasks: %s, %v", data, err)
	}
}

func TestMigrateStateRemovesEmptyQuickTaskFieldWithoutOwner(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"projects":[{"id":"weclaw","name":"WeClaw","root":"/srv/weclaw","quick_tasks":[]}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateState(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil || strings.Contains(string(data), "quick_tasks") {
		t.Fatalf("empty quick_tasks survived migration: %s, %v", data, err)
	}
}

func TestMigrateStateKeepsQuickTasksWhenWorkflowConflicts(t *testing.T) {
	root := t.TempDir()
	accounts := filepath.Join(root, "accounts")
	if err := os.MkdirAll(accounts, 0o700); err != nil {
		t.Fatal(err)
	}
	credential := `{"version":1,"bot_token":"secret","ilink_bot_id":"bot","baseurl":"https://ilinkai.weixin.qq.com","ilink_user_id":"owner-1"}`
	if err := os.WriteFile(filepath.Join(accounts, "bot.json"), []byte(credential), 0o600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "config.json")
	configData := `{"projects":[{"id":"weclaw","name":"WeClaw","root":"/srv/weclaw","quick_tasks":[{"id":"review","name":"审查改动","prompt":"配置内容"}]}]}`
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := workflow.NewStore(filepath.Join(root, "workflows.json"), []string{"weclaw"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Import("owner-1", workflow.Definition{
		ID: workflow.StableImportID("weclaw", "review"), ProjectID: "weclaw", Name: "审查改动",
		PromptTemplate: "用户已修改内容", Slots: []workflow.Slot{}, CreatedAt: 1, UpdatedAt: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := migrateState(root); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("migration conflict error = %v", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil || !strings.Contains(string(data), "quick_tasks") {
		t.Fatalf("conflicting migration removed source: %s, %v", data, err)
	}
}

func TestDeploymentSnapshotRestoresManagedStateAndLeavesWorkspace(t *testing.T) {
	base := t.TempDir()
	stateRoot := filepath.Join(base, "state")
	deploymentDir := filepath.Join(stateRoot, "deployments", "tx")
	binaryPath := filepath.Join(base, "bin", "weclaw")
	unitPath := filepath.Join(base, "units", "weclaw.service")
	mustWriteTestFile(t, filepath.Join(stateRoot, "config.json"), "old-config", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "task-history.json"), "old-history", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "weclaw.log"), "old-log", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "accounts", "owner.sync.json"), "old-sync", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "tasks", "index.json"), "old-queue", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "workspace", "user.txt"), "user-work", 0o600)
	mustWriteTestFile(t, binaryPath, "old-binary", 0o755)
	mustWriteTestFile(t, unitPath, "old-unit", 0o644)

	snapshot, err := createDeploymentSnapshot(deploymentDir, stateRoot, binaryPath, unitPath)
	if err != nil {
		t.Fatalf("createDeploymentSnapshot() error = %v", err)
	}
	mustWriteTestFile(t, filepath.Join(stateRoot, "config.json"), "new-config", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "new-state.json"), "new-state", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "weclaw.log"), "new-log", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "workspace", "user.txt"), "new-user-work", 0o600)
	mustWriteTestFile(t, binaryPath, "new-binary", 0o755)
	mustWriteTestFile(t, unitPath, "new-unit", 0o644)

	if err := restoreDeploymentSnapshot(snapshot, stateRoot, binaryPath, unitPath); err != nil {
		t.Fatalf("restoreDeploymentSnapshot() error = %v", err)
	}
	assertTestFile(t, filepath.Join(stateRoot, "config.json"), "old-config")
	assertTestFile(t, filepath.Join(stateRoot, "accounts", "owner.sync.json"), "old-sync")
	assertTestFile(t, filepath.Join(stateRoot, "tasks", "index.json"), "old-queue")
	assertTestFile(t, filepath.Join(stateRoot, "weclaw.log"), "new-log")
	assertTestFile(t, filepath.Join(stateRoot, "workspace", "user.txt"), "new-user-work")
	assertTestFile(t, binaryPath, "old-binary")
	assertTestFile(t, unitPath, "old-unit")
	if _, err := os.Stat(filepath.Join(stateRoot, "new-state.json")); !os.IsNotExist(err) {
		t.Fatalf("new managed state survived rollback: %v", err)
	}
	if _, err := os.Stat(filepath.Join(deploymentDir, "config.old")); err != nil {
		t.Fatalf("long-lived config backup missing: %v", err)
	}
}

func TestDeploymentSnapshotRejectsTampering(t *testing.T) {
	base := t.TempDir()
	stateRoot := filepath.Join(base, "state")
	deploymentDir := filepath.Join(stateRoot, "deployments", "tx")
	binaryPath := filepath.Join(base, "weclaw")
	unitPath := filepath.Join(base, "weclaw.service")
	mustWriteTestFile(t, filepath.Join(stateRoot, "config.json"), "config", 0o600)
	mustWriteTestFile(t, binaryPath, "binary", 0o755)
	mustWriteTestFile(t, unitPath, "unit", 0o644)
	snapshot, err := createDeploymentSnapshot(deploymentDir, stateRoot, binaryPath, unitPath)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteTestFile(t, filepath.Join(snapshot.StatePath, "config.json"), "tampered", 0o600)
	if err := restoreDeploymentSnapshot(snapshot, stateRoot, binaryPath, unitPath); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("restoreDeploymentSnapshot() error = %v", err)
	}
}

func TestRewriteSystemdUnitRemovesLegacyForeground(t *testing.T) {
	path := filepath.Join(t.TempDir(), "weclaw.service")
	content := "[Service]\nExecStart=/old/weclaw start --foreground --api-addr 127.0.0.1:18011\nRestart=always\n"
	mustWriteTestFile(t, path, content, 0o644)
	if err := rewriteSystemdUnit(path, "/new/weclaw", true); err != nil {
		t.Fatalf("rewriteSystemdUnit() error = %v", err)
	}
	assertTestFile(t, path, "[Service]\nExecStart=/new/weclaw start --draining\nRestart=always\n")
	if err := rewriteSystemdUnit(path, "/new/weclaw", false); err != nil {
		t.Fatalf("rewriteSystemdUnit(normal) error = %v", err)
	}
	assertTestFile(t, path, "[Service]\nExecStart=/new/weclaw start\nRestart=always\n")
}

func TestInspectCandidateVersionRequiresExactPlatformMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "candidate")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' '{\"version\":\"v2.5.0-test.1\",\"goos\":\"%s\",\"goarch\":\"%s\"}'\n", runtime.GOOS, runtime.GOARCH)
	mustWriteTestFile(t, path, script, 0o700)
	metadata, err := inspectCandidateVersion(context.Background(), path)
	if err != nil || metadata.Version != "v2.5.0-test.1" {
		t.Fatalf("inspectCandidateVersion() = %#v, %v", metadata, err)
	}
	mustWriteTestFile(t, path, "#!/bin/sh\nprintf '%s\\n' '{\"version\":\"v2.5.0-test.1\",\"goos\":\"other\",\"goarch\":\"other\"}'\n", 0o700)
	if _, err := inspectCandidateVersion(context.Background(), path); err == nil || !strings.Contains(err.Error(), "platform") {
		t.Fatalf("inspectCandidateVersion() platform error = %v", err)
	}
}

func TestVerifyReleaseChecksum(t *testing.T) {
	path := filepath.Join(t.TempDir(), "weclaw_linux_amd64")
	content := []byte("verified release")
	mustWriteTestFile(t, path, string(content), 0o600)
	sum := sha256.Sum256(content)
	manifest := []byte(fmt.Sprintf("%x  weclaw_linux_amd64\n", sum))
	if got, err := verifyReleaseChecksum(path, "weclaw_linux_amd64", manifest); err != nil || got != fmt.Sprintf("%x", sum) {
		t.Fatalf("verifyReleaseChecksum() = %q, %v", got, err)
	}
	if _, err := verifyReleaseChecksum(path, "weclaw_linux_arm64", manifest); err == nil {
		t.Fatal("verifyReleaseChecksum() accepted a missing platform artifact")
	}
}

func TestValidateDeployOptionsSeparatesReleaseAndLocalModes(t *testing.T) {
	base := deployOptions{Service: defaultServiceName, Timeout: time.Minute, TargetBinary: "/tmp/weclaw", StateRoot: "/tmp/state"}
	release := base
	release.ReleaseVersion = "v2.5.0-test.1"
	if err := validateDeployOptions(release); err != nil {
		t.Fatalf("release options rejected: %v", err)
	}
	local := base
	local.Binary, local.Expected = "/tmp/candidate", "v2.5.0-test.1"
	if err := validateDeployOptions(local); err != nil {
		t.Fatalf("local options rejected: %v", err)
	}
	local.ReleaseVersion = "v2.5.0-test.1"
	if err := validateDeployOptions(local); err == nil {
		t.Fatal("mixed release and local options accepted")
	}
}

func mustWriteTestFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertTestFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}
