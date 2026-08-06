package messaging

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLibraryPersistsLinksAndPrivateDeliveryCopies(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "library.json")
	store, err := NewLibraryStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RecordLink("owner", "project", "设计参考", "https://example.com/design"); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "report.pdf")
	if err := os.WriteFile(source, []byte("pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	delivery, err := store.RecordDelivery("owner", "project", source)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.FilePath == source || !filepath.IsAbs(delivery.FilePath) {
		t.Fatalf("delivery path = %q", delivery.FilePath)
	}
	if info, err := os.Stat(delivery.FilePath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("delivery copy = %v, %v", info, err)
	}
	reopened, err := NewLibraryStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.List("owner", LibraryLink)) != 1 || len(reopened.List("owner", LibraryDelivery)) != 1 {
		t.Fatalf("reopened records = %#v", reopened.List("owner", ""))
	}
	if len(reopened.List("other", "")) != 0 {
		t.Fatal("library ownership leaked")
	}
}
