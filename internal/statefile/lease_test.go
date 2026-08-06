package statefile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLeaseRejectsConcurrentRuntimeAndMigration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	runtimeLease, err := Acquire(root, LeaseRuntime)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeLease.Close()
	if _, err := Acquire(root, LeaseMigration); ErrorCategory(err) != CategoryConflict {
		t.Fatalf("concurrent lease error = %v, category = %q", err, ErrorCategory(err))
	}
	if err := runtimeLease.Close(); err != nil {
		t.Fatal(err)
	}
	migrationLease, err := Acquire(root, LeaseMigration)
	if err != nil {
		t.Fatal(err)
	}
	defer migrationLease.Close()
	info, err := os.Stat(filepath.Join(root, ".state.lock"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("lease file = %v, err=%v", info, err)
	}
}

func TestLeaseRejectsUnsafeRootAndSymlink(t *testing.T) {
	if _, err := Acquire("/", LeaseRuntime); ErrorCategory(err) != CategoryPermission {
		t.Fatalf("root lease error = %v", err)
	}
	base := t.TempDir()
	realRoot := filepath.Join(base, "real")
	if err := os.Mkdir(realRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	linkRoot := filepath.Join(base, "link")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(linkRoot, LeaseRuntime); ErrorCategory(err) != CategoryPermission {
		t.Fatalf("symlink root error = %v, category = %q", err, ErrorCategory(err))
	}
}
