package messaging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollectArtifactsOnlyReturnsSupportedOutboxFiles(t *testing.T) {
	outbox := t.TempDir()
	paths := []string{
		filepath.Join(outbox, "report.pdf"),
		filepath.Join(outbox, "change.patch"),
		filepath.Join(outbox, "bundle.zip"),
	}
	for _, path := range paths {
		if err := os.WriteFile(path, []byte("deliverable"), 0o600); err != nil {
			t.Fatalf("write artifact: %v", err)
		}
	}
	if err := os.WriteFile(filepath.Join(outbox, "program.exe"), []byte("MZ"), 0o600); err != nil {
		t.Fatalf("write unsupported artifact: %v", err)
	}

	got, err := collectArtifacts(outbox)
	if err != nil {
		t.Fatalf("collectArtifacts() error: %v", err)
	}
	if len(got.Paths) != len(paths) {
		t.Fatalf("artifact paths = %#v, want %d", got.Paths, len(paths))
	}
	if len(got.Skipped) != 1 || !strings.Contains(got.Skipped[0], "program.exe") {
		t.Fatalf("skipped = %#v", got.Skipped)
	}
}

func TestCollectArtifactsRejectsSymlinkAndOversizedFile(t *testing.T) {
	outbox := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if err := os.Symlink(outside, filepath.Join(outbox, "leak.txt")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	large := filepath.Join(outbox, "large.zip")
	if err := os.WriteFile(large, nil, 0o600); err != nil {
		t.Fatalf("create large file: %v", err)
	}
	if err := os.Truncate(large, maxOutboundArtifactBytes+1); err != nil {
		t.Fatalf("truncate large file: %v", err)
	}

	got, err := collectArtifacts(outbox)
	if err != nil {
		t.Fatalf("collectArtifacts() error: %v", err)
	}
	if len(got.Paths) != 0 || len(got.Skipped) != 2 {
		t.Fatalf("collection = %#v", got)
	}
}

func TestAppendArtifactSummary(t *testing.T) {
	got := appendArtifactSummary("任务完成", []string{"/tmp/report.pdf"}, []string{"bundle.zip（上传失败）"})
	if !strings.Contains(got, "已发送附件：report.pdf") {
		t.Fatalf("missing sent summary: %q", got)
	}
	if !strings.Contains(got, "附件未发送：bundle.zip（上传失败）") {
		t.Fatalf("missing failure summary: %q", got)
	}
}
