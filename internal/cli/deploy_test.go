package cli

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
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
	for _, name := range []string{"task-history.json", "codex-link-clawbot.pid"} {
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
	for _, name := range []string{"task-history.json", "codex-link-clawbot.pid"} {
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

func TestMigrateStateRejectsPreRenameConfigurations(t *testing.T) {
	tests := []struct {
		name   string
		config string
	}{
		{name: "flat", config: `{"codex":{"command":"codex"},"projects":[{"id":"app","name":"App","root":"/srv/app"}]}`},
		{name: "v2", config: `{"schema_version":2,"codex":{"command":"codex"},"codex-link-clawbot":{}}`},
		{name: "v3", config: `{"schema_version":3,"codex":{"command":"codex"},"codex-link-clawbot":{}}`},
		{name: "old brand key", config: `{"schema_version":5,"codex":{"command":"codex"},"weclaw":{}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(test.config), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := migrateState(root); err == nil {
				t.Fatal("pre-rename configuration was accepted")
			}
		})
	}
}

func TestMigrateStateAcceptsCurrentConfigurationV5(t *testing.T) {
	root := t.TempDir()
	current := `{"schema_version":5,"codex":{"command":"codex"},"codex-link-clawbot":{"project_entries":[],"reply":{},"security":{}}}`
	path := filepath.Join(root, "config.json")
	if err := os.WriteFile(path, []byte(current), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := migrateState(root); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("current config mode = %v", info.Mode().Perm())
	}
}

func TestMigrateStateResetsLegacyDeliveryRecords(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "library.json")
	archive := filepath.Join(root, "deliveries", "owner")
	if err := os.MkdirAll(archive, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archive, "report.pdf"), []byte("pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	v1 := `{"version":1,"owners":{"owner":[{"id":"link","kind":"link","title":"参考","url":"https://example.com","created_at":1},{"id":"file","kind":"delivery","project_id":"app","title":"report.pdf","file_path":"/srv/deliveries/report.pdf","size":3,"created_at":2}]}}`
	if err := os.WriteFile(path, []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateState(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version": 3`) || !strings.Contains(string(data), `"owners": {}`) || strings.Contains(string(data), `"id": "file"`) {
		t.Fatalf("migrated delivery library = %s", data)
	}
	if _, err := os.Stat(filepath.Join(root, "deliveries")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy delivery archive still exists: %v", err)
	}
}

func TestMigrateStateDestroysProjectMonitoringState(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"automation-state.json", "project-watch-state.json", "scheduled-reports-state.json"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(`{"retired":true}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrateState(root); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"automation-state.json", "project-watch-state.json", "scheduled-reports-state.json"} {
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("retired monitoring state %s still exists: %v", name, err)
		}
	}
}

func TestMigrateStateRemovesOnlyProjectWatchNotices(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "pending-notices.json")
	state := `{"version":1,"owners":{"owner":[{"id":"11111111111111111111111111111111","kind":"project_watch","dedup_key":"watch:daily","title":"项目关注","body":"异常","created_at":1,"expires_at":2},{"id":"22222222222222222222222222222222","kind":"deployment","dedup_key":"deploy:v4","title":"部署完成","body":"版本已更新","created_at":1,"expires_at":2}]}}`
	if err := os.WriteFile(path, []byte(state), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateState(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "project_watch") || strings.Contains(string(data), "watch:daily") || !strings.Contains(string(data), "deployment") {
		t.Fatalf("migrated pending notices = %s", data)
	}
}

func TestMigrateStateReplacesLegacyControlStateWithEmptyV13(t *testing.T) {
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
	if string(data) != "{\n  \"version\": 13,\n  \"owners\": {},\n  \"receipts\": {}\n}\n" {
		t.Fatalf("migrated control state = %s", data)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("control state mode = %v, %v", info.Mode().Perm(), err)
	}
}

func TestMigrateStateReplacesV2ControlStateWithEmptyV13(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "control-state.json")
	legacy := `{"version":2,"owners":{"owner":{"revision":"0123456789abcdef0123456789abcdef"}},"receipts":{}}`
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
	if string(data) != "{\n  \"version\": 13,\n  \"owners\": {},\n  \"receipts\": {}\n}\n" {
		t.Fatalf("migrated control state = %s", data)
	}
}

func TestMigrateStateReplacesV12ControlStateWithEmptyV13(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "control-state.json")
	legacy := `{"version":12,"owners":{"owner":{"revision":"0123456789abcdef0123456789abcdef"}},"receipts":{}}`
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
	if string(data) != "{\n  \"version\": 13,\n  \"owners\": {},\n  \"receipts\": {}\n}\n" {
		t.Fatalf("migrated control state = %s", data)
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

func TestMigrateStateDestroysRetiredWorkflowFile(t *testing.T) {
	root := t.TempDir()
	configData := `{"schema_version":5,"codex":{"command":"codex"},"codex-link-clawbot":{"project_entries":[{"id":"project","name":"Project","root":"/srv/project"}],"reply":{},"security":{}}}`
	configPath := filepath.Join(root, "config.json")
	if err := os.WriteFile(configPath, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	workflowPath := filepath.Join(root, "workflows.json")
	if err := os.WriteFile(workflowPath, []byte(`{"version":1,"owners":{"owner-1":{}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := migrateState(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(workflowPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired workflow state still exists: %v", err)
	}
	if err := migrateState(root); err != nil {
		t.Fatalf("idempotent prompt template removal error = %v", err)
	}
}

func TestMigrateStateRejectsUnsafePromptTemplateState(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.json")
	if err := os.WriteFile(target, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "workflows.json")); err != nil {
		t.Fatal(err)
	}
	if err := migrateState(root); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("unsafe workflow state error = %v", err)
	}
}

func TestDeploymentSnapshotRestoresManagedStateAndLeavesWorkspace(t *testing.T) {
	base := t.TempDir()
	stateRoot := filepath.Join(base, "state")
	deploymentDir := filepath.Join(stateRoot, "deployments", "tx")
	binaryPath := filepath.Join(base, "bin", "codex-link-clawbot")
	unitPath := filepath.Join(base, "units", "codex-link-clawbot.service")
	mustWriteTestFile(t, filepath.Join(stateRoot, "config.json"), "old-config", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "task-history.json"), "old-history", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "codex-link-clawbot.log"), "old-log", 0o600)
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
	mustWriteTestFile(t, filepath.Join(stateRoot, "codex-link-clawbot.log"), "new-log", 0o600)
	mustWriteTestFile(t, filepath.Join(stateRoot, "workspace", "user.txt"), "new-user-work", 0o600)
	mustWriteTestFile(t, binaryPath, "new-binary", 0o755)
	mustWriteTestFile(t, unitPath, "new-unit", 0o644)

	if err := restoreDeploymentSnapshot(snapshot, stateRoot, binaryPath, unitPath); err != nil {
		t.Fatalf("restoreDeploymentSnapshot() error = %v", err)
	}
	assertTestFile(t, filepath.Join(stateRoot, "config.json"), "old-config")
	assertTestFile(t, filepath.Join(stateRoot, "accounts", "owner.sync.json"), "old-sync")
	assertTestFile(t, filepath.Join(stateRoot, "tasks", "index.json"), "old-queue")
	assertTestFile(t, filepath.Join(stateRoot, "codex-link-clawbot.log"), "new-log")
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
	binaryPath := filepath.Join(base, "codex-link-clawbot")
	unitPath := filepath.Join(base, "codex-link-clawbot.service")
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
	path := filepath.Join(t.TempDir(), "codex-link-clawbot.service")
	content := "[Service]\nExecStart=/old/codex-link-clawbot start --foreground --api-addr 127.0.0.1:18011\nRestart=always\n"
	mustWriteTestFile(t, path, content, 0o644)
	if err := rewriteSystemdUnit(path, "/new/codex-link-clawbot", true); err != nil {
		t.Fatalf("rewriteSystemdUnit() error = %v", err)
	}
	assertTestFile(t, path, "[Service]\nExecStart=/new/codex-link-clawbot start --draining\nRestart=always\n")
	if err := rewriteSystemdUnit(path, "/new/codex-link-clawbot", false); err != nil {
		t.Fatalf("rewriteSystemdUnit(normal) error = %v", err)
	}
	assertTestFile(t, path, "[Service]\nExecStart=/new/codex-link-clawbot start\nRestart=always\n")
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
	path := filepath.Join(t.TempDir(), "codex-link-clawbot_linux_amd64")
	content := []byte("verified release")
	mustWriteTestFile(t, path, string(content), 0o600)
	sum := sha256.Sum256(content)
	manifest := []byte(fmt.Sprintf("%x  codex-link-clawbot_linux_amd64\n", sum))
	if got, err := verifyReleaseChecksum(path, "codex-link-clawbot_linux_amd64", manifest); err != nil || got != fmt.Sprintf("%x", sum) {
		t.Fatalf("verifyReleaseChecksum() = %q, %v", got, err)
	}
	if _, err := verifyReleaseChecksum(path, "codex-link-clawbot_linux_arm64", manifest); err == nil {
		t.Fatal("verifyReleaseChecksum() accepted a missing platform artifact")
	}
}

func TestValidateDeployOptionsSeparatesReleaseAndLocalModes(t *testing.T) {
	base := deployOptions{Service: defaultServiceName, Timeout: time.Minute, TargetBinary: "/tmp/codex-link-clawbot", StateRoot: "/tmp/state"}
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
