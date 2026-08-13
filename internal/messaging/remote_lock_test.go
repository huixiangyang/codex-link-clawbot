package messaging

import (
	"context"
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

func TestCommandDirectoryRemoteLockRequiresConfirmation(t *testing.T) {
	lock, err := NewRemoteLock(filepath.Join(t.TempDir(), "remote-lock.json"), "private-code")
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestHandler(t)
	handler.SetRemoteLock(lock)
	handler.openMainMenu(context.Background(), "owner-1")
	prompt, handled := handler.handleControlInput(context.Background(), "owner-1", "43", false, nextTestControlSource())
	if !handled || !strings.HasPrefix(prompt.Text, "呈现与安全") {
		t.Fatalf("settings center = %#v handled=%v", prompt, handled)
	}
	prompt, handled = handler.handleControlInput(context.Background(), "owner-1", "2", false, nextTestControlSource())
	if !handled || !strings.Contains(prompt.Text, "准备远程锁定") || lock.IsLocked("owner-1") {
		t.Fatalf("lock confirmation = %#v handled=%v locked=%v", prompt, handled, lock.IsLocked("owner-1"))
	}
	state, status, err := handler.controlStates.Load("owner-1")
	if err != nil || status != controlStateActive || state.View != viewSecurityLockConfirm {
		t.Fatalf("lock confirmation state = %#v status=%v err=%v", state, status, err)
	}

	locked, handled := handler.handleControlInput(context.Background(), "owner-1", "1", false, nextTestControlSource())
	if !handled || !strings.Contains(locked.Text, "已远程锁定") || !lock.IsLocked("owner-1") {
		t.Fatalf("confirmed lock = %#v handled=%v locked=%v", locked, handled, lock.IsLocked("owner-1"))
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
