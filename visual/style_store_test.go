package visual

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStyleCatalogAndResolution(t *testing.T) {
	styles := Styles()
	if len(styles) != 5 || styles[0].ID != StyleEditorial || styles[1].ID != StyleAtelier || styles[2].ID != StyleNoir || styles[3].ID != StyleCute || styles[4].ID != StyleMinimal {
		t.Fatalf("style catalog = %#v", styles)
	}
	for input, want := range map[string]Style{
		"刊物": StyleEditorial, "编辑部": StyleEditorial,
		"构筑": StyleAtelier, "建筑": StyleAtelier,
		"黑标": StyleNoir, "黑金": StyleNoir,
		"可爱": StyleCute, "奶油": StyleCute,
		"简洁": StyleMinimal, "极简": StyleMinimal,
	} {
		got, ok := ResolveStyle(input)
		if !ok || got != want {
			t.Fatalf("ResolveStyle(%q) = %q, %v", input, got, ok)
		}
	}
	if got, ok := ResolveStyle("霓虹 AI"); ok || got != "" {
		t.Fatalf("unknown style = %q, %v", got, ok)
	}
}

func TestStyleStorePersistsPrivateOwnerPreferences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "private", "visual-styles.json")
	store, err := NewStyleStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := store.Get("owner-1"); got != DefaultStyle {
		t.Fatalf("default style = %q", got)
	}
	if err := store.Set("owner-1", StyleEditorial); err != nil {
		t.Fatal(err)
	}
	if err := store.Set("owner-2", StyleNoir); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewStyleStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.Get("owner-1"); got != StyleEditorial {
		t.Fatalf("owner-1 style = %q", got)
	}
	if got := reloaded.Get("owner-2"); got != StyleNoir {
		t.Fatalf("owner-2 style = %q", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("style file mode = %v", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("style directory mode = %v", dirInfo.Mode().Perm())
	}
	if err := store.Set("owner-1", Style("neon")); err == nil {
		t.Fatal("invalid style was persisted")
	}
}

func TestStyleStoreRejectsUnknownAndInvalidData(t *testing.T) {
	tests := []string{
		`{"version":1,"owners":{},"extra":true}`,
		`{"version":2,"owners":{}}`,
		`{"version":1,"owners":{"owner-1":"neon"}}`,
		`{"version":1,"owners":{"":"atelier"}}`,
		`{"version":1,"owners":{}} trailing`,
	}
	for index, data := range tests {
		path := filepath.Join(t.TempDir(), "styles.json")
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := NewStyleStore(path); err == nil {
			t.Fatalf("invalid style data %d was accepted", index)
		}
	}
}
