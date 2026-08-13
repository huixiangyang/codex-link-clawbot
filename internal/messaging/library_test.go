package messaging

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeliveryStorePersistsPrivateCopies(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "library.json")
	store, err := NewDeliveryStore(path)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "report.pdf")
	if err := os.WriteFile(source, []byte("pdf"), 0o600); err != nil {
		t.Fatal(err)
	}
	delivery, err := store.RecordDelivery("owner", DeliverySource{ProjectID: "project", ThreadID: "thread-1", TaskID: "task-1"}, source)
	if err != nil {
		t.Fatal(err)
	}
	if delivery.FilePath == source || !filepath.IsAbs(delivery.FilePath) {
		t.Fatalf("delivery path = %q", delivery.FilePath)
	}
	if info, err := os.Stat(delivery.FilePath); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("delivery copy = %v, %v", info, err)
	}
	reopened, err := NewDeliveryStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.List("owner")) != 1 {
		t.Fatalf("reopened records = %#v", reopened.List("owner"))
	}
	if len(reopened.List("other")) != 0 {
		t.Fatal("delivery ownership leaked")
	}
	verified, availability, exists := reopened.Verify("owner", delivery.ID)
	if !exists || availability != DeliveryAvailable || verified.ThreadID != "thread-1" || verified.TaskID != "task-1" {
		t.Fatalf("verified delivery = %#v, %q, %v", verified, availability, exists)
	}
	if err := os.WriteFile(delivery.FilePath, []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, availability, exists = reopened.Verify("owner", delivery.ID)
	if !exists || availability != DeliveryUnavailable {
		t.Fatalf("tampered delivery availability = %q, %v", availability, exists)
	}
	summary := reopened.SummaryForThread("owner", "project", "thread-1")
	if !summary.Available || summary.Total != 1 || summary.Resendable != 0 || summary.Unavailable != 1 {
		t.Fatalf("thread summary = %#v", summary)
	}
}

func TestDeliveryDetailMarksCorruptedCopyUnrecoverable(t *testing.T) {
	handler, _ := newSessionHandler(t)
	store, err := NewDeliveryStore(filepath.Join(t.TempDir(), "library.json"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "artifact.txt")
	if err := os.WriteFile(source, []byte("good"), 0o600); err != nil {
		t.Fatal(err)
	}
	record, err := store.RecordDelivery("owner-1", DeliverySource{ProjectID: "workspace", ThreadID: "thread-source", TaskID: "task-source"}, source)
	if err != nil {
		t.Fatal(err)
	}
	handler.SetDeliveryStore(store)
	if err := os.WriteFile(record.FilePath, []byte("evil"), 0o600); err != nil {
		t.Fatal(err)
	}
	detail := handler.openDeliveryDetail("owner-1", record.ID, 1)
	for _, want := range []string{"状态：已失效", "不可恢复", "不会静默重跑原请求", "Codex 线程："} {
		if !strings.Contains(detail, want) {
			t.Fatalf("detail missing %q: %q", want, detail)
		}
	}
	if strings.Contains(detail, "再次发送") {
		t.Fatalf("unavailable delivery offered resend: %q", detail)
	}
}

func TestDeliveryStorePrunesMetadataAndPrivateCopyTogether(t *testing.T) {
	root := t.TempDir()
	store, err := NewDeliveryStore(filepath.Join(root, "library.json"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "artifact.txt")
	if err := os.WriteFile(source, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	var oldestPath string
	for index := 0; index <= deliveryStoreLimit; index++ {
		record, recordErr := store.RecordDelivery("owner", DeliverySource{
			ProjectID: "workspace", ThreadID: "thread-source", TaskID: fmt.Sprintf("task-%d", index),
		}, source)
		if recordErr != nil {
			t.Fatal(recordErr)
		}
		if index == 0 {
			oldestPath = record.FilePath
		}
	}
	if records := store.List("owner"); len(records) != deliveryStoreLimit {
		t.Fatalf("records = %d", len(records))
	}
	if _, err := os.Stat(oldestPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("pruned private copy still exists: %v", err)
	}
}

func TestDeliveryStoreRejectsLegacySchemaAtRuntime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "library.json")
	if err := os.WriteFile(path, []byte(`{"version":2,"owners":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDeliveryStore(path); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Fatalf("legacy delivery schema error = %v", err)
	}
}
