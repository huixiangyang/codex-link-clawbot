package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestManagerPersistsProjectSelection(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	path := filepath.Join(t.TempDir(), "project-state.json")
	projects := []Definition{
		{ID: "alpha", Name: "Alpha", Root: rootA},
		{ID: "beta", Name: "Beta", Root: rootB},
	}
	manager, err := NewManager(projects, path)
	if err != nil {
		t.Fatal(err)
	}
	if got := manager.Current("owner").ID; got != "alpha" {
		t.Fatalf("default project = %q", got)
	}
	resolved, err := manager.Resolve("bet")
	if err != nil || resolved.ID != "beta" {
		t.Fatalf("Resolve() = %#v, %v", resolved, err)
	}
	if _, err := manager.Select("owner", "beta"); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewManager(projects, path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Current("owner").ID; got != "beta" {
		t.Fatalf("persisted project = %q", got)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state permissions = %v, %v", info, err)
	}
}

func TestManagerRejectsMissingRootAndUnknownProject(t *testing.T) {
	_, err := NewManager([]Definition{{ID: "missing", Name: "Missing", Root: filepath.Join(t.TempDir(), "absent")}}, filepath.Join(t.TempDir(), "state.json"))
	if err == nil {
		t.Fatal("missing root should be rejected")
	}
	manager, err := NewManager([]Definition{{ID: "valid", Name: "Valid", Root: t.TempDir()}}, filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Select("owner", "missing"); !errors.Is(err, ErrUnknownProject) {
		t.Fatalf("Select() error = %v", err)
	}
}
