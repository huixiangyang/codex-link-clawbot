package statefile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManifestBuildCopyAndVerify(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "a.json"), []byte("alpha"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "b.json"), []byte("beta"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := BuildManifest(source, []string{"nested/b.json", "a.json"}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Entries) != 2 || manifest.Entries[0].Path != "a.json" || manifest.Entries[1].Path != "nested/b.json" {
		t.Fatalf("manifest = %#v", manifest)
	}
	if err := VerifyManifest(source, manifest, 1024); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "backup")
	if err := CopyManifestFiles(source, destination, manifest, 1024); err != nil {
		t.Fatal(err)
	}
	if err := VerifyManifest(destination, manifest, 1024); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(destination, "a.json"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyManifest(destination, manifest, 1024); ErrorCategory(err) != CategoryCorrupt {
		t.Fatalf("modified backup error = %v, category = %q", err, ErrorCategory(err))
	}
}
