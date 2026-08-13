package messaging

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/runtimecontrol"
	"github.com/huixiangyang/codex-link-clawbot/internal/statefile"
	"github.com/huixiangyang/codex-link-clawbot/internal/taskqueue"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
)

type fixedRuntimeLifecycle struct {
	snapshot runtimecontrol.Snapshot
}

func (*fixedRuntimeLifecycle) BeginIngress() {}
func (*fixedRuntimeLifecycle) EndIngress()   {}
func (lifecycle *fixedRuntimeLifecycle) Snapshot() runtimecontrol.Snapshot {
	return lifecycle.snapshot
}

func newDiagnosticHandler(t *testing.T) (*Handler, *taskqueue.Store, *fixedRuntimeLifecycle) {
	t.Helper()
	statefile.ClearLastFailure()
	t.Cleanup(statefile.ClearLastFailure)
	store, err := taskqueue.NewStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := &fixedRuntimeLifecycle{snapshot: runtimecontrol.Snapshot{
		Status: runtimecontrol.StateReady,
		Codex:  runtimecontrol.ComponentStatus{Ready: true},
		WeChat: runtimecontrol.WeChatStatus{
			Monitors: 1, Healthy: 1, OldestPendingSeconds: -1, LastSuccessSecondsAgo: 0,
		},
	}}
	handler := NewHandler(nil)
	handler.SetTaskQueue(store, nil)
	handler.SetRuntimeLifecycle(lifecycle)
	// 测试基线只观察初始化完成后的运行期错误。
	statefile.ClearLastFailure()
	return handler, store, lifecycle
}

func enqueueDiagnosticTask(t *testing.T, store *taskqueue.Store, ownerID, source string) taskqueue.Task {
	t.Helper()
	task, duplicate, err := store.Enqueue(taskqueue.EnqueueInput{
		SourceMessageKey: source,
		OwnerID:          ownerID,
		ProjectID:        "workspace",
		Summary:          "诊断测试",
		Text:             "执行诊断测试",
		ResponseMode:     preference.ResponseAdaptive,
		VisualStyle:      visual.StyleEditorial,
	})
	if err != nil || duplicate {
		t.Fatalf("enqueue diagnostic task: duplicate=%v err=%v", duplicate, err)
	}
	return task
}

func TestNoReplyDiagnosticExplainsPausedQueueWithoutLeakingIdentity(t *testing.T) {
	handler, store, _ := newDiagnosticHandler(t)
	ownerID := "owner-private-value"
	if err := store.SetPaused(ownerID, true); err != nil {
		t.Fatal(err)
	}

	got := handler.buildNoReplyDiagnostic(ownerID)
	for _, want := range []string{"codex-link-clawbot 请求队列已暂停", "继续队列", "请求队列"} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostic missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, ownerID) {
		t.Fatalf("diagnostic leaked owner identity: %q", got)
	}
}

func TestNoReplyDiagnosticExplainsAmbiguousDelivery(t *testing.T) {
	handler, store, _ := newDiagnosticHandler(t)
	queued := enqueueDiagnosticTask(t, store, "owner-1", "message-1")
	if _, claimed, err := store.ClaimNext(nil); err != nil || !claimed {
		t.Fatalf("claim task: claimed=%v err=%v", claimed, err)
	}
	if _, err := store.Finish("owner-1", queued.ID, taskqueue.StateInterrupted, taskqueue.ReasonDeliveryAmbiguous); err != nil {
		t.Fatal(err)
	}

	got := handler.buildNoReplyDiagnostic("owner-1")
	for _, want := range []string{"发送结果不确定", "不会自动补发", "取回冻结文字"} {
		if !strings.Contains(got, want) {
			t.Fatalf("diagnostic missing %q: %q", want, got)
		}
	}
}

func TestNoReplyDiagnosticDoesNotExposeLiveTaskStage(t *testing.T) {
	handler, store, _ := newDiagnosticHandler(t)
	queued := enqueueDiagnosticTask(t, store, "owner-1", "message-live")
	if _, claimed, err := store.ClaimNext(nil); err != nil || !claimed {
		t.Fatalf("claim task: claimed=%v err=%v", claimed, err)
	}
	privateStage := "正在处理 /private/workspace 的敏感步骤"
	if err := store.UpdateStage("owner-1", queued.ID, privateStage); err != nil {
		t.Fatal(err)
	}

	got := handler.buildNoReplyDiagnostic("owner-1")
	if !strings.Contains(got, "codex-link-clawbot 执行状态：运行中") || strings.Contains(got, privateStage) || strings.Contains(got, "/private/workspace") {
		t.Fatalf("live task diagnostic = %q", got)
	}
}

func TestNoReplyDiagnosticIgnoresCurrentInboundBatchButReportsStuckBatch(t *testing.T) {
	handler, _, lifecycle := newDiagnosticHandler(t)
	lifecycle.snapshot.WeChat.PendingBatches = 1
	lifecycle.snapshot.WeChat.OldestPendingSeconds = 0
	got := handler.buildNoReplyDiagnostic("owner-1")
	if !strings.Contains(got, "没有发现确定性阻断") || strings.Contains(got, "批次长时间未提交") {
		t.Fatalf("current inbound batch was misdiagnosed: %q", got)
	}

	lifecycle.snapshot.WeChat.OldestPendingSeconds = 31
	got = handler.buildNoReplyDiagnostic("owner-1")
	if !strings.Contains(got, "批次长时间未提交") || !strings.Contains(got, "待提交批次：1") {
		t.Fatalf("stuck batch diagnostic = %q", got)
	}
}

func TestNoReplyDiagnosticUsesSanitizedStateFailure(t *testing.T) {
	handler, _, _ := newDiagnosticHandler(t)
	privatePath := filepath.Join(t.TempDir(), "private-secret.json")
	if err := statefile.Write(privatePath, []byte("oversized"), statefile.Options{MaxBytes: 1}); err == nil {
		t.Fatal("expected capacity failure")
	}

	got := handler.buildNoReplyDiagnostic("owner-secret")
	if !strings.Contains(got, "最近一次持久化失败") || !strings.Contains(got, "容量不足") {
		t.Fatalf("state failure diagnostic = %q", got)
	}
	for _, forbidden := range []string{privatePath, "private-secret.json", "owner-secret", "oversized"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("diagnostic leaked %q: %q", forbidden, got)
		}
	}
}

func TestLockedOwnerCanRequestNoReplyDiagnostic(t *testing.T) {
	var sent ilink.SendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/ilink/bot/sendmessage" {
			http.NotFound(response, request)
			return
		}
		if err := json.NewDecoder(request.Body).Decode(&sent); err != nil {
			t.Errorf("decode diagnostic: %v", err)
		}
		_, _ = response.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	lock, err := NewRemoteLock(filepath.Join(t.TempDir(), "remote-lock.json"), "private-code")
	if err != nil {
		t.Fatal(err)
	}
	if err := lock.Lock("owner-1"); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(nil)
	handler.SetRemoteLock(lock)
	client := ilink.NewClient(&ilink.Credentials{
		BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL,
	})
	err = handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
		MessageID: 1, FromUserID: "owner-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish, ContextToken: "context",
		ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "为什么没回复"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sent.Msg.ItemList) != 1 || sent.Msg.ItemList[0].TextItem == nil {
		t.Fatalf("sent diagnostic = %#v", sent.Msg.ItemList)
	}
	got := sent.Msg.ItemList[0].TextItem.Text
	if !strings.Contains(got, "已被远程锁定") || strings.Contains(got, "private-code") {
		t.Fatalf("locked diagnostic = %q", got)
	}
}
