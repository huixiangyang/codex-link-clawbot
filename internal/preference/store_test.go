package preference

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
)

func TestStorePersistsUnifiedOwnerPreferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "preferences.json")
	store, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Get("owner-1"); got != DefaultOwnerPreferences() {
		t.Fatalf("default preferences = %#v", got)
	}
	if err := store.SetStyle("owner-1", visual.StyleEditorial); err != nil {
		t.Fatal(err)
	}
	if err := store.SetResponseMode("owner-1", ResponseVoice); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Get("owner-1"); got.Style != visual.StyleEditorial || got.ResponseMode != ResponseVoice {
		t.Fatalf("reloaded preferences = %#v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("preferences mode = %v", info.Mode().Perm())
	}
	if dirInfo, err := os.Stat(filepath.Dir(path)); err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("preferences directory = %#v err=%v", dirInfo, err)
	}
}

func TestStoreRejectsUnknownAndInvalidData(t *testing.T) {
	tests := []string{
		`{"version":1,"owners":{},"extra":true}`,
		`{"version":2,"owners":{}}`,
		`{"version":1,"owners":{"owner-1":{"style":"neon","response_mode":"adaptive"}}}`,
		`{"version":1,"owners":{"owner-1":{"style":"atelier","response_mode":"unknown"}}}`,
		`{"version":1,"owners":{"":{"style":"atelier","response_mode":"adaptive"}}}`,
		`{"version":1,"owners":{}} trailing`,
	}
	for index, data := range tests {
		path := filepath.Join(t.TempDir(), "preferences.json")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewStore(path); err == nil {
			t.Fatalf("invalid preferences %d were accepted", index)
		}
	}
}

func TestResponseModeCatalog(t *testing.T) {
	modes := ResponseModes()
	if len(modes) != 3 || modes[0].ID != ResponseAdaptive || modes[1].ID != ResponseReading || modes[2].ID != ResponseVoice {
		t.Fatalf("response modes = %#v", modes)
	}
}
