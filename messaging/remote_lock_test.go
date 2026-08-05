package messaging

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestRemoteLockPersistsAndRequiresExactCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remote-lock.json")
	lock, err := NewRemoteLock(path, "private-code")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Lock("owner"); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewRemoteLock(path, "private-code")
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.IsLocked("owner") {
		t.Fatal("lock state was not persisted")
	}
	if err := reopened.Unlock("owner", "wrong-code"); err == nil {
		t.Fatal("wrong code should fail")
	}
	if err := reopened.Unlock("owner", "private-code"); err != nil || reopened.IsLocked("owner") {
		t.Fatalf("unlock error = %v", err)
	}
}

func TestImageAnnotationIntent(t *testing.T) {
	for _, text := range []string{"帮我批注这张图", "在图上标注问题", "标注图片"} {
		if !isImageAnnotationIntent(text) {
			t.Fatalf("annotation intent not detected: %q", text)
		}
	}
	if isImageAnnotationIntent("分析图片内容") || !strings.Contains(mutationBusyText(), "切换项目") {
		t.Fatal("ordinary image analysis should not enter annotation mode")
	}
}
