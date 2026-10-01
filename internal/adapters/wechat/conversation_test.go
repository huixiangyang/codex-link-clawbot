package wechat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
)

func submitTestDraft(t *testing.T, h *testHandler, client *ilink.Client, owner string, id int64, text string) {
	t.Helper()
	for index, value := range []string{text, "提交"} {
		msg := ilink.WeixinMessage{MessageID: id + int64(index), FromUserID: owner, MessageType: ilink.MessageTypeUser, MessageState: ilink.MessageStateFinish, ContextToken: "fresh", ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: value}}}}
		if err := h.HandleMessage(context.Background(), client, msg); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDraftNeverRunsAutomaticallyAndKeepsTarget(t *testing.T) {
	f := newNumberMenuFixture(t)
	msg := ilink.WeixinMessage{MessageID: 100, FromUserID: "owner", MessageType: ilink.MessageTypeUser, MessageState: ilink.MessageStateFinish, ContextToken: "fresh", ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeImage, ImageItem: &ilink.ImageItem{URL: "http://127.0.0.1:1/not-downloaded.png"}}}}
	for range 2 {
		if err := f.h.HandleMessage(context.Background(), f.client, msg); err != nil {
			t.Fatal(err)
		}
	}
	textID := f.input(t, "只描述图片，不修改文件")
	s, err := f.h.loadDrafts()
	if err != nil {
		t.Fatal(err)
	}
	draft := s.Owners["owner"]
	if len(draft.Attachments.Images) != 1 || draft.Text != "只描述图片，不修改文件" || len(f.h.tasks.List("owner")) != 0 {
		t.Fatal("draft executed or duplicated")
	}
	// 每次读取磁盘，重建处理器后也只能显式提交；目标切换不能转移草稿。
	if _, err = f.h.targets.NewConversation("owner", draft.Target.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	f.input(t, "提交")
	if len(f.h.tasks.List("owner")) != 0 {
		t.Fatal("draft submitted into another target")
	}
	f.input(t, "丢弃草稿")
	f.send(t, textID, "只描述图片，不修改文件")
	if len(f.h.tasks.List("owner")) != 0 {
		t.Fatal("replayed draft text became executable work")
	}
	// 过期只清除草稿，不自动执行；来源回执继续阻止重投复活。
	draft.ExpiresAt = time.Now().Add(-time.Minute).Unix()
	s.Owners["owner"] = draft
	if err = f.h.saveDrafts(s); err != nil {
		t.Fatal(err)
	}
	if err = f.h.expireDrafts(); err != nil {
		t.Fatal(err)
	}
	s, err = f.h.loadDrafts()
	if err != nil || len(s.Owners) != 0 {
		t.Fatal("draft expiry failed")
	}
}

func TestPlainDigitsAreWorkAndInvalidTargetHasRecovery(t *testing.T) {
	f := newNumberMenuFixture(t)
	f.input(t, "#")
	f.input(t, "0")
	task := waitForTerminalTask(t, f.h.tasks, "owner")
	input, err := f.h.tasks.LoadRequest("owner", task.ID)
	if err != nil || input.Text != "0" {
		t.Fatal("plain digit intercepted as menu")
	}
	f.input(t, "/")
	if f.h.menus.session("owner").location.kind != "home" {
		t.Fatal("prefixed navigation failed")
	}
	f.agent.mu.Lock()
	delete(f.agent.threads, task.ThreadID)
	f.agent.mu.Unlock()
	f.input(t, "继续处理")
	page := f.h.menus.session("owner").page
	if page.Title != "会话暂不可用" || page.actions[3].kind != "new" || len(f.h.tasks.List("owner")) != 1 {
		t.Fatal("missing target lacks recovery")
	}
}

func TestUnsupportedOfficeFileIsNotStaged(t *testing.T) {
	f := newNumberMenuFixture(t)
	msg := ilink.WeixinMessage{MessageID: 100, FromUserID: "owner", MessageType: ilink.MessageTypeUser, MessageState: ilink.MessageStateFinish, ContextToken: "fresh", ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeFile, FileItem: &ilink.FileItem{FileName: "report.docx", URL: "http://127.0.0.1:1/never"}}}}
	if err := f.h.HandleMessage(context.Background(), f.client, msg); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !strings.Contains(f.messages[len(f.messages)-1].ItemList[0].TextItem.Text, "导出为 PDF") || len(f.h.tasks.List("owner")) != 0 {
		t.Fatal("unsupported file should have actionable rejection")
	}
}

func TestResultShortcutsReadFilesAndSelectWithoutExecution(t *testing.T) {
	f := newNumberMenuFixture(t)
	task := f.enqueue(t, "原会话工作")
	native, err := f.agent.StartThread(context.Background(), f.h.projects.List()[0].Root)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.h.targets.Bind("owner", task.TargetID, native.ID); err != nil {
		t.Fatal(err)
	}
	if err = f.h.tasks.AttachThread("owner", task.ID, native.ID); err != nil {
		t.Fatal(err)
	}
	outbox, err := f.h.tasks.PrepareOutbox("owner", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(outbox, "answer.txt")
	if err = os.WriteFile(file, []byte("saved artifact"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = f.h.tasks.FreezeResult("owner", task.ID, request.FreezeResultInput{Reply: strings.Repeat("完整回答", 400), ArtifactPaths: []string{file}}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.h.tasks.Finish("owner", task.ID, request.StateSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if _, err = f.h.targets.NewConversation("owner", task.ProjectID); err != nil {
		t.Fatal(err)
	}
	current := f.h.targets.Current("owner")
	f.input(t, "全文 "+shortTaskID(task.ID))
	if f.h.menus.session("owner").location.kind != "read" || f.h.targets.Current("owner") != current {
		t.Fatal("reading changed input target")
	}
	f.mu.Lock()
	before := len(f.messages)
	f.mu.Unlock()
	id := f.input(t, "文件 "+shortTaskID(task.ID))
	f.send(t, id, "文件 "+shortTaskID(task.ID))
	f.mu.Lock()
	files := 0
	for _, message := range f.messages[before:] {
		for _, item := range message.ItemList {
			if item.Type == ilink.ItemTypeFile {
				files++
			}
			if item.TextItem != nil && strings.Contains(item.TextItem.Text, "完整回答") {
				t.Error("file-only retrieval resent the answer")
			}
		}
	}
	f.mu.Unlock()
	if files != 1 {
		t.Fatalf("file delivery count = %d", files)
	}
	f.input(t, "继续会话 "+shortTaskID(task.ID))
	if f.h.targets.Current("owner").ThreadID != native.ID || len(f.h.tasks.List("owner")) != 1 {
		t.Fatal("continuation must select, not execute")
	}
}

func TestFinalReplyOnlyLabelsOtherConversationsAndNecessaryActions(t *testing.T) {
	f := newNumberMenuFixture(t)
	task := f.enqueue(t, "原会话工作")
	result := request.Result{Reply: "处理完成"}
	message := ilink.WeixinMessage{FromUserID: "owner", ContextToken: "fresh"}
	send := func() string {
		t.Helper()
		report := f.h.sendCompletedTask(context.Background(), f.client, message, task, result, NewClientID())
		if report.Outcome != request.DeliverySucceeded {
			t.Fatalf("send failed: %#v", report)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		return f.messages[len(f.messages)-1].ItemList[0].TextItem.Text
	}
	if got := send(); got != result.Reply {
		t.Fatalf("current answer contains boilerplate: %q", got)
	}
	result.Artifacts = []request.ResultArtifact{{Name: "answer.txt"}}
	if got := send(); !strings.Contains(got, "文件 "+shortTaskID(task.ID)) || strings.Contains(got, "全文 ") {
		t.Fatalf("short answer should only offer file retrieval: %q", got)
	}
	result.Artifacts = nil
	result.Reply = strings.Repeat("完整回答", 160)
	if got := send(); !strings.Contains(got, "回答已节选") || !strings.Contains(got, "全文 "+shortTaskID(task.ID)) {
		t.Fatalf("excerpt missing full-text action: %q", got)
	}
	result.Reply = "处理完成"
	if _, err := f.h.targets.NewConversation("owner", task.ProjectID); err != nil {
		t.Fatal(err)
	}
	if got := send(); !strings.HasPrefix(got, "结果 "+shortTaskID(task.ID)+" · 主项目") || !strings.Contains(got, result.Reply) {
		t.Fatalf("late result lost conversation context: %q", got)
	}
}
