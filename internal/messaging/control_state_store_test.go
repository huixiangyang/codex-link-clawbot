package messaging

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNumberedMenuContinuesAfterHandlerRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control-state.json")
	firstStore, err := NewControlStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	first := NewHandler(newHandlerThreadClient())
	first.SetControlStateStore(firstStore)
	attachTestSessionManager(t, first)
	menu, handled := first.handleControlInput(context.Background(), "owner-1", "/", false, nextTestControlSource())
	if !handled || !strings.Contains(menu.Text, "WeClaw 操作总览") || !strings.Contains(menu.Text, "11  新建会话") {
		t.Fatalf("main menu = %#v, handled=%v", menu, handled)
	}
	before, status, err := firstStore.Load("owner-1")
	if err != nil || status != controlStateActive || before.View != viewSystemMain || len(before.Options) != 40 || before.Options[1].Code != "11" {
		t.Fatalf("stored main menu = %#v, status=%v, err=%v", before, status, err)
	}

	restartedStore, err := NewControlStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewHandler(newHandlerThreadClient())
	restarted.SetControlStateStore(restartedStore)
	result, handled := restarted.handleControlInput(context.Background(), "owner-1", "63", false, nextTestControlSource())
	if !handled || result.Domain != DomainSystem || !strings.Contains(result.Text, "使用说明") {
		t.Fatalf("resumed menu action = %#v, handled=%v", result, handled)
	}
	after, status, err := restartedStore.Load("owner-1")
	if err != nil || status != controlStateActive || after.View != viewSystemGuide || after.Revision == before.Revision {
		t.Fatalf("next menu revision = %#v, status=%v, err=%v", after, status, err)
	}
}

func TestPendingSessionInputContinuesAfterHandlerRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control-state.json")
	runtime := newHandlerThreadClient()
	firstStore, err := NewControlStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	first := NewHandler(runtime)
	first.SetControlStateStore(firstStore)
	attachTestSessionManager(t, first)
	prompt, handled := first.handleControlInput(
		context.Background(), "owner-1", "新建会话", false, nextTestControlSource(),
	)
	if !handled || !strings.Contains(prompt.Text, "会话名称") {
		t.Fatalf("new session prompt = %#v, handled=%v", prompt, handled)
	}

	restartedStore, err := NewControlStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewHandler(runtime)
	restarted.SetControlStateStore(restartedStore)
	restarted.SetSessionManager(first.sessions)
	created, handled := restarted.handleControlInput(
		context.Background(), "owner-1", "重启后的会话", false, nextTestControlSource(),
	)
	if !handled || !strings.Contains(created.Text, "已创建并切换") || runtime.next != 1 {
		t.Fatalf("resumed input = %#v, handled=%v next=%d", created, handled, runtime.next)
	}
}

func TestControlStateStoreSurvivesRestartWithoutDisplayContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control-state.json")
	store, err := NewControlStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	expiresAt := time.Now().Add(time.Hour).Truncate(time.Second)
	written, err := store.Put("owner-1", controlState{
		View: "session.list", Mode: controlChoice,
		ExpiresAt: expiresAt,
		Options: []controlOption{
			{Code: "7", Label: "下一页 · 2/3", Action: actionSessionPage, Page: 2, Query: "登录"},
			{Code: "15", Label: "打开详情", Action: actionSessionDetail, Value: "thread-1", Page: 1},
		},
		Back: controlOption{Action: actionSessionMenu},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !validControlRevision(written.Revision) {
		t.Fatalf("revision = %q", written.Revision)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"不得落盘", "下一页", "打开详情"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("display content %q leaked into state: %s", forbidden, raw)
		}
	}

	reloaded, err := NewControlStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	state, status, err := reloaded.Load("owner-1")
	if err != nil || status != controlStateActive {
		t.Fatalf("Load() status=%v err=%v", status, err)
	}
	if state.Revision != written.Revision || state.View != "session.list" || len(state.Options) != 2 {
		t.Fatalf("reloaded state = %#v", state)
	}
	if state.Options[0].Label != "" || state.Options[0].Navigate != navigationNext || state.Options[0].Code != "7" || state.Options[1].Code != "15" {
		t.Fatalf("display-only fields survived reload: %#v", state)
	}
	if state.Options[0].Query != "登录" || state.Options[1].Value != "thread-1" {
		t.Fatalf("action parameters = %#v", state.Options)
	}

	deleted, err := reloaded.CompareAndDelete("owner-1", "00000000000000000000000000000000")
	if err != nil || deleted {
		t.Fatalf("wrong revision delete = %v, %v", deleted, err)
	}
	deleted, err = reloaded.CompareAndDelete("owner-1", state.Revision)
	if err != nil || !deleted {
		t.Fatalf("matching revision delete = %v, %v", deleted, err)
	}
}

func TestControlStateStoreExpiresAndDeletesState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control-state.json")
	store, err := NewControlStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	store.now = func() time.Time { return now }
	if _, err := store.Put("owner-1", controlState{
		View: "system.main", Mode: controlChoice, ExpiresAt: now.Add(time.Second),
		Options: []controlOption{{Action: actionGuide}}, Back: controlOption{Action: actionExit},
	}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	if state, status, err := store.Load("owner-1"); err != nil || status != controlStateExpired || state != nil {
		t.Fatalf("expired Load() = %#v, %v, %v", state, status, err)
	}
	reloaded, err := NewControlStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if state, status, err := reloaded.Load("owner-1"); err != nil || status != controlStateMissing || state != nil {
		t.Fatalf("expired state remained on disk: %#v, %v, %v", state, status, err)
	}
}

func TestControlReceiptPreventsDuplicateActionAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control-state.json")
	store, err := NewControlStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	reserved, err := store.ReserveReceipt("owner-1", "account:message:42", string(IntentRemoteLock), DomainSecurity)
	if err != nil || !reserved {
		t.Fatalf("first reserve = %v, %v", reserved, err)
	}
	reserved, err = store.ReserveReceipt("owner-1", "account:message:42", string(IntentRemoteLock), DomainSecurity)
	if err != nil || reserved {
		t.Fatalf("duplicate reserve = %v, %v", reserved, err)
	}
	if reserved, err = store.ReserveReceipt("owner-1", "account:message:42", string(IntentQueuePause), DomainTask); err == nil || reserved {
		t.Fatalf("conflicting reserve = %v, %v", reserved, err)
	}

	reloaded, err := NewControlStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	receipt, exists := reloaded.FindReceipt("owner-1", "account:message:42")
	if !exists || receipt.ActionID != string(IntentRemoteLock) || receipt.Domain != DomainSecurity {
		t.Fatalf("reloaded receipt = %#v, exists=%v", receipt, exists)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "owner-1") {
		t.Fatalf("raw owner id leaked into receipt: %s", raw)
	}
}

func TestControlStateConsumeAndReceiptCommitTogether(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control-state.json")
	store, err := NewControlStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Put("owner-1", controlState{
		View: viewProjectCenter, Mode: controlChoice,
		Options: []controlOption{{Action: actionSelectProject, Value: "beta"}},
		Back:    controlOption{Action: actionMain},
	})
	if err != nil {
		t.Fatal(err)
	}
	consumed, duplicate, err := store.ConsumeAndReserve(
		"owner-1", state.Revision, "account:message:81", string(actionSelectProject), DomainProject,
	)
	if err != nil || !consumed || duplicate {
		t.Fatalf("ConsumeAndReserve() = consumed:%v duplicate:%v err:%v", consumed, duplicate, err)
	}
	reloaded, err := NewControlStateStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, status, err := reloaded.Load("owner-1"); err != nil || status != controlStateMissing {
		t.Fatalf("consumed menu remained: status=%v err=%v", status, err)
	}
	if _, exists := reloaded.FindReceipt("owner-1", "account:message:81"); !exists {
		t.Fatal("receipt did not commit with menu consumption")
	}
	consumed, duplicate, err = reloaded.ConsumeAndReserve(
		"owner-1", state.Revision, "account:message:81", string(actionSelectProject), DomainProject,
	)
	if err != nil || consumed || !duplicate {
		t.Fatalf("duplicate ConsumeAndReserve() = consumed:%v duplicate:%v err:%v", consumed, duplicate, err)
	}
}

func TestControlReceiptRollbackRestoresRevision(t *testing.T) {
	store, err := NewControlStateStore(filepath.Join(t.TempDir(), "control-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Put("owner-1", controlState{
		View: viewProjectQuickTasks, Mode: controlChoice,
		Options: []controlOption{{Action: actionRunQuickTask, Value: "review"}},
		Back:    controlOption{Action: actionProjectCenter},
	})
	if err != nil {
		t.Fatal(err)
	}
	consumed, duplicate, err := store.ConsumeAndReserve(
		"owner-1", state.Revision, "account:message:82", string(actionRunQuickTask), DomainProject,
	)
	if err != nil || !consumed || duplicate {
		t.Fatalf("ConsumeAndReserve() = %v, %v, %v", consumed, duplicate, err)
	}
	if err := store.RollbackConsumedReceipt("owner-1", "account:message:82", *state); err != nil {
		t.Fatal(err)
	}
	restored, status, err := store.Load("owner-1")
	if err != nil || status != controlStateActive || restored.Revision != state.Revision {
		t.Fatalf("restored state = %#v, status=%v err=%v", restored, status, err)
	}
	if _, exists := store.FindReceipt("owner-1", "account:message:82"); exists {
		t.Fatal("rolled-back receipt remained active")
	}
}

func TestReservedControlReceiptRollbackRemovesDirectRerun(t *testing.T) {
	store, err := NewControlStateStore(filepath.Join(t.TempDir(), "control-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if reserved, err := store.ReserveReceipt(
		"owner-1", "account:message:83", string(IntentTaskRerun), DomainTask,
	); err != nil || !reserved {
		t.Fatalf("ReserveReceipt() = %v, %v", reserved, err)
	}
	if err := store.RollbackReservedReceipt(
		"owner-1", "account:message:83", string(IntentTaskRerun), DomainTask,
	); err != nil {
		t.Fatal(err)
	}
	if _, exists := store.FindReceipt("owner-1", "account:message:83"); exists {
		t.Fatal("rolled-back direct rerun receipt remained active")
	}
}

func TestControlStateStoreRejectsDuplicateExplicitCodes(t *testing.T) {
	store, err := NewControlStateStore(filepath.Join(t.TempDir(), "control-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Put("owner-1", controlState{
		View: viewSystemMain, Mode: controlChoice,
		Options: []controlOption{
			{Code: "11", Action: actionPromptNewSession},
			{Code: "11", Action: actionPromptRenameSession},
		},
		Back: controlOption{Action: actionExit},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicated control option code") {
		t.Fatalf("duplicate explicit codes error = %v", err)
	}
}

func TestControlStateStoreRejectsUnknownSchema(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control-state.json")
	if err := os.WriteFile(path, []byte(`{"version":2,"owners":{},"receipts":{},"legacy":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewControlStateStore(path); err == nil {
		t.Fatal("unknown control state field was accepted")
	}
}
