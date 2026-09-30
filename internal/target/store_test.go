package target

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestIntentSurvivesExecutionSwitchAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "targets.json")
	store, err := Open(path, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Capture("owner")
	if err != nil {
		t.Fatal(err)
	}
	next, err := store.Capture("owner")
	if err != nil || next.ID != first.ID {
		t.Fatalf("continuous intent = %+v, %v", next, err)
	}
	switched, err := store.Select("owner", "beta", func() (string, error) { return "thread-beta", nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Bind("owner", first.ID, "thread-alpha"); err != nil {
		t.Fatal(err)
	}
	if store.Current("owner") != switched {
		t.Fatal("late binding stole the current target")
	}
	if _, err := store.Select("owner", "alpha", func() (string, error) { return "", errors.New("remote unavailable") }); err == nil {
		t.Fatal("failed selection accepted")
	}
	reopened, err := Open(path, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	if reopened.Current("owner") != switched {
		t.Fatal("failed selection changed persisted target")
	}
	resolved, err := reopened.Resolve("owner", first.ID)
	if err != nil || resolved.ThreadID != "thread-alpha" {
		t.Fatalf("frozen intent = %+v, %v", resolved, err)
	}
	if _, err := reopened.Resolve("another-owner", first.ID); err == nil {
		t.Fatal("foreign intent exposed")
	}
}
