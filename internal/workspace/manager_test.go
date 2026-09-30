package workspace

import (
	"path/filepath"
	"testing"
)

func TestWorkspaceWhitelist(t *testing.T) {
	if _, err := NewManager([]Definition{{ID: "missing", Root: filepath.Join(t.TempDir(), "missing")}}); err == nil {
		t.Fatal("missing directory accepted")
	}
	m, err := NewManager([]Definition{{ID: "workspace", Root: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get("outside"); ok {
		t.Fatal("unconfigured workspace accepted")
	}
}
