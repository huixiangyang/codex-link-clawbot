package statefile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testState struct {
	Version int    `json:"version"`
	Value   string `json:"value"`
}

func TestWriteRollbackAtEveryFaultBoundary(t *testing.T) {
	for _, point := range []FaultPoint{FaultWrite, FaultFileSync, FaultRename, FaultDirectorySync} {
		t.Run(string(point), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private", "state.json")
			if err := WriteJSON(path, testState{Version: 1, Value: "old"}, Options{}); err != nil {
				t.Fatal(err)
			}
			injected := errors.New("injected failure")
			err := WriteJSON(path, testState{Version: 1, Value: "new"}, Options{
				Fault: func(current FaultPoint) error {
					if current == point {
						return injected
					}
					return nil
				},
			})
			if err == nil || ErrorCategory(err) != CategoryUnavailable {
				t.Fatalf("fault error = %v, category = %q", err, ErrorCategory(err))
			}
			var state testState
			found, err := ReadJSON(path, &state, Options{})
			if err != nil || !found || state.Value != "old" {
				t.Fatalf("state after %s = %#v, found=%v, err=%v", point, state, found, err)
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".codex-link-clawbot-state-") || strings.HasPrefix(entry.Name(), ".codex-link-clawbot-backup-") {
					t.Fatalf("orphan transaction file remains: %s", entry.Name())
				}
			}
		})
	}
}

func TestStrictJSONSecurityAndCategories(t *testing.T) {
	tests := []struct {
		name     string
		data     string
		maxBytes int64
		want     Category
	}{
		{name: "unknown", data: `{"version":1,"value":"ok","extra":true}`, want: CategorySchema},
		{name: "trailing", data: `{"version":1,"value":"ok"} trailing`, want: CategorySchema},
		{name: "corrupt", data: `{"version":1,"value":`, want: CategoryCorrupt},
		{name: "capacity", data: `{"version":1,"value":"too large"}`, maxBytes: 8, want: CategoryCapacity},
	}
	for _, item := range tests {
		t.Run(item.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(item.data), 0o644); err != nil {
				t.Fatal(err)
			}
			var state testState
			_, err := ReadJSON(path, &state, Options{MaxBytes: item.maxBytes})
			if ErrorCategory(err) != item.want {
				t.Fatalf("error = %v, category = %q, want %q", err, ErrorCategory(err), item.want)
			}
		})
	}

	directory := t.TempDir()
	target := filepath.Join(directory, "target.json")
	if err := os.WriteFile(target, []byte(`{"version":1,"value":"ok"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "link.json")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	var state testState
	if _, err := ReadJSON(link, &state, Options{}); ErrorCategory(err) != CategoryPermission {
		t.Fatalf("symlink error = %v, category = %q", err, ErrorCategory(err))
	}
}

func TestWriteProtectsFileAndDirectoryAndCleansOrphans(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "state.json")
	orphan := filepath.Join(directory, ".codex-link-clawbot-state-state.json-stale")
	if err := os.WriteFile(orphan, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteJSON(path, testState{Version: 1, Value: "saved"}, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan still exists: %v", err)
	}
	fileInfo, err := os.Stat(path)
	if err != nil || fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %v, err=%v", fileInfo, err)
	}
	directoryInfo, err := os.Stat(directory)
	if err != nil || directoryInfo.Mode().Perm() != 0o700 {
		t.Fatalf("directory mode = %v, err=%v", directoryInfo, err)
	}
}

func TestWriteRejectsSymlinkAncestorBeforeCreatingState(t *testing.T) {
	base := t.TempDir()
	realDirectory := filepath.Join(base, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedDirectory := filepath.Join(base, "linked")
	if err := os.Symlink(realDirectory, linkedDirectory); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(linkedDirectory, "nested", "state.json")
	if err := WriteJSON(path, testState{Version: 1, Value: "unsafe"}, Options{}); ErrorCategory(err) != CategoryPermission {
		t.Fatalf("symlink ancestor error = %v, category = %q", err, ErrorCategory(err))
	}
	if _, err := os.Stat(filepath.Join(realDirectory, "nested")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state path was created through symlink: %v", err)
	}
}
