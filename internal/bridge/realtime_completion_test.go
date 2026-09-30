package bridge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/request"
)

func TestSlowAttachmentRemainsVisibleAndInterruptible(t *testing.T) {
	f := newNumberMenuFixture(t)
	var calls atomic.Int32
	f.agent.chat = func(context.Context, string, codex.ChatRequest) (string, error) {
		calls.Add(1)
		return "unexpected", nil
	}
	started := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { started <- struct{}{}; <-r.Context().Done() }))
	t.Cleanup(func() { server.CloseClientConnections(); server.Close() })
	f.sequence = 1
	done := make(chan error, 1)
	go func() {
		done <- f.h.HandleMessage(context.Background(), f.client, ilink.WeixinMessage{MessageID: 1, FromUserID: "owner", MessageType: ilink.MessageTypeUser, MessageState: ilink.MessageStateFinish, ContextToken: "fresh", ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeImage, ImageItem: &ilink.ImageItem{URL: server.URL + "/image.png"}}}})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("attachment blocked message ingress")
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("download never started")
	}
	task := f.h.tasks.List("owner")[0]
	if !task.InputPending || !strings.Contains(task.Stage, "附件") {
		t.Fatalf("preparation invisible: %+v", task)
	}
	f.input(t, "不要等待执行这条指令")
	if f.h.menus.session("owner").page.Title != "提交失败" || len(f.h.tasks.List("owner")) != 1 {
		t.Fatal("busy input accepted")
	}
	f.input(t, "2")
	f.input(t, "1")
	terminal := waitForTerminalTask(t, f.h.tasks, "owner")
	if terminal.State != request.StateCancelled || calls.Load() != 0 {
		t.Fatalf("cancel did not stop preparation: %+v calls=%d", terminal, calls.Load())
	}
}

func TestArchiveFailureRestoresCompletedWorkWithoutReexecution(t *testing.T) {
	f := newNumberMenuFixture(t)
	var calls atomic.Int32
	f.agent.chat = func(_ context.Context, _ string, input codex.ChatRequest) (string, error) {
		calls.Add(1)
		if err := os.WriteFile(filepath.Join(input.ArtifactDir, "answer.txt"), []byte("已完成成果"), 0600); err != nil {
			return "", err
		}
		// 用目录占据清单路径，真实触发归档失败，而不是执行失败。
		if err := os.Mkdir(filepath.Join(filepath.Dir(input.ArtifactDir), "result.json"), 0700); err != nil {
			return "", err
		}
		return "已经完成，请保存成果", nil
	}
	f.input(t, "完成工作")
	task := waitForTerminalTask(t, f.h.tasks, "owner")
	if task.State != request.StateSucceeded || !task.ArchiveFailed || task.ExecutionCompletedAt == 0 {
		t.Fatalf("lost execution success: %+v", task)
	}
	if _, err := f.h.tasks.Retry("owner", task.ID, "must-not-retry", "fresh"); err == nil {
		t.Fatal("completed work was retryable")
	}
	if err := os.Remove(filepath.Join(f.h.tasks.Root(), task.ID, "result.json")); err != nil {
		t.Fatal(err)
	}
	reopened, err := request.NewStore(f.h.tasks.Root())
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint, err := reopened.LoadCompletion("owner", task.ID); err != nil || checkpoint.Reply != "已经完成，请保存成果" {
		t.Fatalf("completion lost across restart: %+v %v", checkpoint, err)
	}
	runtime := &Runtime{Handler: f.h.Handler, Coordinator: f.h.coordinator}
	if err := runtime.RestoreResult(context.Background(), "owner", task.ID); err != nil {
		t.Fatal(err)
	}
	result, err := f.h.tasks.LoadResult("owner", task.ID)
	if err != nil || len(result.Artifacts) != 1 || calls.Load() != 1 {
		t.Fatalf("restore: %+v %v calls=%d", result, err, calls.Load())
	}
	restored, _ := f.h.tasks.Find("owner", task.ID)
	if restored.ArchiveFailed || restored.Reason != "" {
		t.Fatalf("stale failure: %+v", restored)
	}
	// 同一失败投递编号重放时必须返回原失败，不发送第二次，也不谎报成功。
	op := uuid.NewString()
	if _, _, err := f.h.tasks.BeginRedelivery("owner", task.ID, op); err != nil {
		t.Fatal(err)
	}
	if err := f.h.tasks.RecordDelivery("owner", task.ID, request.DeliveryReceipt{OperationID: op, Outcome: request.DeliveryExplicitFailure, FailureCode: request.ReasonDeliveryFailed, AttemptedAt: time.Now().Unix()}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	before := len(f.messages)
	f.mu.Unlock()
	if err := runtime.Redeliver(context.Background(), "owner", task.ID, op); err == nil {
		t.Fatal("failed delivery replay reported success")
	}
	f.mu.Lock()
	after := len(f.messages)
	f.mu.Unlock()
	if after != before {
		t.Fatal("delivery replay sent new messages")
	}
}

func TestUnconfirmedInterruptDoesNotBecomeCancelledOrRetryable(t *testing.T) {
	f := newNumberMenuFixture(t)
	started := make(chan struct{})
	f.agent.chat = func(ctx context.Context, _ string, _ codex.ChatRequest) (string, error) {
		close(started)
		<-ctx.Done()
		return "", codex.ErrInterruptUnconfirmed
	}
	f.input(t, "正在执行")
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("not started")
	}
	task := f.h.tasks.List("owner")[0]
	if !f.h.coordinator.Cancel("owner", task.ID) {
		t.Fatal("cancel not requested")
	}
	task = waitForTerminalTask(t, f.h.tasks, "owner")
	if task.ExecutionStatus() != "unknown" || task.Reason != request.ReasonInterruptUnconfirmed {
		t.Fatalf("false cancellation: %+v", task)
	}
	_, err := f.h.tasks.Retry("owner", task.ID, "retry", "fresh")
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("unexpected retry outcome: %v", err)
	}
}

func TestRunningMenuIncludesExternalSessionsWithoutSwitchingTarget(t *testing.T) {
	f := newNumberMenuFixture(t)
	f.input(t, "0")
	current := f.h.targets.Current("owner")
	native, err := f.agent.StartThread(context.Background(), f.h.projects.List()[0].Root)
	if err != nil {
		t.Fatal(err)
	}
	f.agent.mu.Lock()
	native.Status.Type = "active"
	f.agent.threads[native.ID] = native
	f.agent.mu.Unlock()
	f.input(t, "4")
	page := f.h.menus.session("owner").page
	if page.Title != "运行中会话" || page.actions[1].threadID != native.ID {
		t.Fatalf("external running thread absent: %+v", page)
	}
	f.input(t, "1")
	if f.h.targets.Current("owner") != current {
		t.Fatal("inspection changed input target")
	}
	if f.h.menus.session("owner").page.Notice != "会话正在执行" {
		t.Fatal("native session showed idle")
	}
}
