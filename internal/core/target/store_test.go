package target

import (
	"errors"
	"testing"
)

func TestIntentSurvivesExecutionSwitchAndRestart(t *testing.T) {
	path := t.TempDir()
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
	initial := store.Snapshot("owner")
	switched, err := store.SelectThread("owner", "beta", "thread-beta", initial)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Bind("owner", first.ID, "thread-alpha"); err != nil {
		t.Fatal(err)
	}
	if store.Current("owner") != switched {
		t.Fatal("late binding stole the current target")
	}
	if _, err := store.SelectThread("owner", "alpha", "thread-alpha", initial); !errors.Is(err, ErrSelectionChanged) {
		t.Fatalf("stale selection accepted: %v", err)
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
