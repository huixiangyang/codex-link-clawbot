package storage

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

func TestTransactionsConcurrencyPermissionsAndBackup(t *testing.T) {
	root := t.TempDir()
	if err := SetSetting(root, "counter", "0"); err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("rollback")
	if err := Update(root, func(tx *sql.Tx) error {
		if err := Put(tx, "settings", Setting{"counter", "999"}); err != nil {
			return err
		}
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	failures := make(chan error, 16)
	for range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			failures <- Update(root, func(tx *sql.Tx) error {
				var value string
				if err := tx.QueryRow("SELECT value FROM settings WHERE key='counter'").Scan(&value); err != nil {
					return err
				}
				n, err := strconv.Atoi(value)
				if err != nil {
					return err
				}
				return Put(tx, "settings", Setting{"counter", strconv.Itoa(n + 1)})
			})
		}()
	}
	group.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got, _, err := GetSetting(root, "counter"); err != nil || got != "16" {
		t.Fatalf("lost update: %q %v", got, err)
	}
	layout, _ := NewLayout(root)
	db, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("UPDATE settings SET value='16' WHERE key='counter'"); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		info, err := os.Stat(layout.Database() + suffix)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("unsafe SQLite file %s: %v %v", suffix, info, err)
		}
	}
	backupRoot := t.TempDir()
	backupLayout, _ := NewLayout(backupRoot)
	if err := backupLayout.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := Backup(root, backupLayout.Database()); err != nil {
		t.Fatal(err)
	}
	if err := SetSetting(root, "counter", "17"); err != nil {
		t.Fatal(err)
	}
	if got, _, err := GetSetting(backupRoot, "counter"); err != nil || got != "16" {
		t.Fatalf("invalid snapshot: %q %v", got, err)
	}
	if err := Backup(root, backupLayout.Database()); err == nil {
		t.Fatal("backup overwrote existing file")
	}
}

func TestDatabaseRejectsSymlinkAndUnsupportedSchema(t *testing.T) {
	root := t.TempDir()
	layout, _ := NewLayout(root)
	if err := layout.Ensure(); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(target, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, layout.Database()); err != nil {
		t.Fatal(err)
	}
	if db, err := Open(root); err == nil {
		db.Close()
		t.Fatal("followed database symlink")
	}
	if data, err := os.ReadFile(target); err != nil || string(data) != "unchanged" {
		t.Fatal("modified symlink target")
	}
	root = t.TempDir()
	if err := Update(root, func(tx *sql.Tx) error { _, err := tx.Exec("PRAGMA user_version=999"); return err }); err != nil {
		t.Fatal(err)
	}
	if db, err := Open(root); err == nil {
		db.Close()
		t.Fatal("accepted future schema")
	}
}
