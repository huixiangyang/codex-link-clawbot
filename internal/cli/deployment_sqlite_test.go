package cli

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/platform/storage"
)

func TestDeploymentSnapshotRestoresSQLiteAndArtifacts(t *testing.T) {
	root := t.TempDir()
	stateRoot := filepath.Join(root, "instance")
	write := func(value string) {
		t.Helper()
		if err := storage.Update(stateRoot, func(tx *sql.Tx) error {
			return storage.Put(tx, "settings", storage.Setting{Key: "test", Value: value})
		}); err != nil {
			t.Fatal(err)
		}
	}
	write("before")
	artifact := filepath.Join(stateRoot, "data", "artifacts", "requests", "example", "output", "note.txt")
	binary, unit := filepath.Join(root, "binary"), filepath.Join(root, "unit")
	mustWriteTestFile(t, artifact, "original artifact", 0o600)
	logPath := filepath.Join(stateRoot, "logs", "service.log")
	mustWriteTestFile(t, logPath, "old diagnostic", 0o600)
	mustWriteTestFile(t, binary, "binary", 0o700)
	mustWriteTestFile(t, unit, "unit", 0o600)
	snapshot, err := createDeploymentSnapshot(filepath.Join(stateRoot, "backups", "deploy-test"), stateRoot, binary, unit)
	if err != nil {
		t.Fatal(err)
	}
	write("after")
	mustWriteTestFile(t, artifact, "changed artifact", 0o600)
	mustWriteTestFile(t, logPath, "deployment diagnostic", 0o600)
	if err := restoreDeploymentSnapshot(snapshot, stateRoot, binary, unit); err != nil {
		t.Fatal(err)
	}
	value, found, err := storage.GetSetting(stateRoot, "test")
	if err != nil || !found || value != "before" {
		t.Fatalf("rollback setting=%q found=%v err=%v", value, found, err)
	}
	assertTestFile(t, artifact, "original artifact")
	assertTestFile(t, logPath, "deployment diagnostic")
}
