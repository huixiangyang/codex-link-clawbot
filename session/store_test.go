package session

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/huixiangyang/weclaw/codex"
)

func TestStorePersistsOwnershipAndActiveThread(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "session-index.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	now := time.Unix(1000, 0)
	thread := codex.ThreadInfo{ID: "019fcc03-fc8b-7842-a812-a132a87b9898", CreatedAt: 900, UpdatedAt: 950}
	if err := store.Register("owner-1", thread, true, now); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if !store.Owns("owner-1", thread.ID) {
		t.Fatal("registered thread should belong to owner-1")
	}
	if store.Owns("owner-2", thread.ID) {
		t.Fatal("thread ownership leaked to owner-2")
	}
	if active, ok := store.Active("owner-1"); !ok || active != thread.ID {
		t.Fatalf("Active() = %q, %v", active, ok)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("session index permissions = %o, want 600", got)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	if active, ok := reopened.Active("owner-1"); !ok || active != thread.ID {
		t.Fatalf("reopened Active() = %q, %v", active, ok)
	}
	record, err := reopened.Resolve("owner-1", "a87b9898", false)
	if err != nil || record.ID != thread.ID {
		t.Fatalf("Resolve() = %#v, %v", record, err)
	}
}

func TestStoreRejectsCorruptOrUnknownSchema(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{name: "invalid json", data: `{`},
		{name: "unknown version", data: `{"version":2,"owners":{}}`},
		{name: "unknown field", data: `{"version":3,"owners":{},"legacy":true}`},
		{name: "trailing data", data: `{"version":3,"owners":{}} {}`},
		{name: "missing owners", data: `{"version":3}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "session-index.json")
			if err := os.WriteFile(path, []byte(tt.data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenStore(path); err == nil {
				t.Fatal("OpenStore() should reject invalid state")
			}
		})
	}
}

func TestStoreIsolatesSessionsByProject(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "session-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	alpha := codex.ThreadInfo{ID: "019fcc03-fc8b-7842-a812-alpha00000001"}
	beta := codex.ThreadInfo{ID: "019fcc03-fc8b-7842-a812-beta000000002"}
	if err := store.RegisterProject("owner", "alpha", alpha, true, now); err != nil {
		t.Fatal(err)
	}
	if err := store.RegisterProject("owner", "beta", beta, true, now); err != nil {
		t.Fatal(err)
	}
	if got, ok := store.ActiveForProject("owner", "alpha"); !ok || got != alpha.ID {
		t.Fatalf("alpha active = %q, %v", got, ok)
	}
	if got, ok := store.ActiveForProject("owner", "beta"); !ok || got != beta.ID {
		t.Fatalf("beta active = %q, %v", got, ok)
	}
	if _, err := store.ResolveForProject("owner", "alpha", "00000002", false); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("cross-project resolve error = %v", err)
	}
}

func TestStoreRejectsAmbiguousOrForeignShortCode(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "session-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1000, 0)
	for _, id := range []string{
		"019fcc03-fc8b-7842-a812-1111a87b9898",
		"019fcc03-fc8b-7842-a812-2222a87b9898",
	} {
		if err := store.Register("owner-1", codex.ThreadInfo{ID: id}, false, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.Resolve("owner-1", "a87b9898", false); !errors.Is(err, ErrAmbiguousCode) {
		t.Fatalf("Resolve() error = %v, want ErrAmbiguousCode", err)
	}
	if _, err := store.Resolve("owner-2", "a87b9898", false); !errors.Is(err, ErrNotOwned) {
		t.Fatalf("foreign Resolve() error = %v, want ErrNotOwned", err)
	}
}

func TestStoreSerializesConcurrentMutations(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "session-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			id := "019fcc03-fc8b-7842-a812-" + time.Unix(int64(index), 0).UTC().Format("150405")
			if err := store.Register("owner-1", codex.ThreadInfo{ID: id}, false, time.Unix(int64(index+1), 0)); err != nil {
				t.Errorf("Register(%d) error = %v", index, err)
			}
		}(i)
	}
	wg.Wait()
	if got := len(store.Records("owner-1", false)); got != 12 {
		t.Fatalf("record count = %d, want 12", got)
	}
}
