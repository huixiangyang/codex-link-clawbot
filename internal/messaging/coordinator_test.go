package messaging

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/config"
	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/project"
	"github.com/huixiangyang/codex-link-clawbot/internal/taskqueue"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
)

type acknowledgementOrderAgent struct {
	*handlerThreadClient
	called chan struct{}
	once   sync.Once
}

func (agent *acknowledgementOrderAgent) ChatThread(_ context.Context, _ string, _ codex.ChatRequest) (string, error) {
	agent.once.Do(func() { close(agent.called) })
	return "执行完成", nil
}

func TestCoordinatorWaitsForQueueAcknowledgementBeforeExecution(t *testing.T) {
	acknowledgementStarted := make(chan struct{})
	acknowledgementRelease := make(chan struct{})
	var sendCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			http.NotFound(w, r)
			return
		}
		if sendCount.Add(1) == 1 {
			close(acknowledgementStarted)
			<-acknowledgementRelease
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	client := ilink.NewClient(&ilink.Credentials{
		BotToken: "token", ILinkBotID: "bot", ILinkUserID: "owner", BaseURL: server.URL,
	})
	agent := &acknowledgementOrderAgent{handlerThreadClient: newHandlerThreadClient(), called: make(chan struct{})}
	handler := NewHandler(agent)
	attachTestSessionManager(t, handler)
	handler.SetProgressConfig(ProgressConfig{Enabled: false})
	store, stop := attachTestTaskQueue(t, handler, client, "owner")
	defer stop()

	handleErr := make(chan error, 1)
	go func() {
		handleErr <- handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
			MessageID: 92, FromUserID: "owner", ToUserID: "bot",
			MessageType: ilink.MessageTypeUser, MessageState: ilink.MessageStateFinish, ContextToken: "context",
			ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "检查发送顺序"}}},
		})
	}()

	select {
	case <-acknowledgementStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("queue acknowledgement did not start")
	}
	select {
	case <-agent.called:
		t.Fatal("Codex started before the queue acknowledgement completed")
	case <-time.After(100 * time.Millisecond):
	}
	close(acknowledgementRelease)
	if err := <-handleErr; err != nil {
		t.Fatal(err)
	}
	waitForTerminalTask(t, store, "owner")
	select {
	case <-agent.called:
	default:
		t.Fatal("Codex did not start after the queue acknowledgement completed")
	}
}

func TestQueuedTaskKeepsProjectSessionAndPreferenceSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()
	client := ilink.NewClient(&ilink.Credentials{
		BotToken: "token", ILinkBotID: "bot", ILinkUserID: "owner", BaseURL: server.URL,
	})
	runtime := newHandlerThreadClient()
	handler := NewHandler(runtime)
	projects, err := project.NewManager([]config.ProjectConfig{
		{ID: "alpha", Name: "Alpha", Root: t.TempDir()},
		{ID: "beta", Name: "Beta", Root: t.TempDir()},
	}, filepath.Join(t.TempDir(), "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	handler.SetProjectManager(projects)
	attachTestSessionManager(t, handler)
	preferences, err := preference.NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := preferences.SetStyle("owner", visual.StyleNoir); err != nil {
		t.Fatal(err)
	}
	handler.SetPreferenceStore(preferences)
	store, err := taskqueue.NewStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := NewCoordinator(handler, store)
	if err != nil {
		t.Fatal(err)
	}
	handler.SetTaskQueue(store, coordinator)
	created := handler.createSession(context.Background(), "owner", "冻结线程")
	if created == "" {
		t.Fatal("session was not created")
	}
	originalThread := handler.sessions.SnapshotThreadID("owner", "alpha")
	if originalThread == "" {
		t.Fatal("session snapshot is empty")
	}
	message := ilink.WeixinMessage{
		MessageID: 91, FromUserID: "owner", ToUserID: "bot",
		MessageType: ilink.MessageTypeUser, MessageState: ilink.MessageStateFinish, ContextToken: "context",
		ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "检查 Alpha"}}},
	}
	if err := handler.HandleMessage(context.Background(), client, message); err != nil {
		t.Fatal(err)
	}
	if _, err := projects.Select("owner", "beta"); err != nil {
		t.Fatal(err)
	}
	if err := preferences.SetStyle("owner", visual.StyleCute); err != nil {
		t.Fatal(err)
	}
	if err := preferences.SetResponseMode("owner", preference.ResponseVoice); err != nil {
		t.Fatal(err)
	}
	tasks := store.List("owner")
	if len(tasks) != 1 {
		t.Fatalf("queued tasks = %#v", tasks)
	}
	task := tasks[0]
	if task.ProjectID != "alpha" || task.ThreadID != originalThread || task.ResponseMode != preference.ResponseAdaptive || task.VisualStyle != visual.StyleNoir {
		t.Fatalf("task snapshot changed after UI selection: %#v", task)
	}
}

func TestCoordinatorCancellationWinsDuringPreflight(t *testing.T) {
	handler := NewHandler(newHandlerThreadClient())
	store, err := taskqueue.NewStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	queued, _, err := store.Enqueue(taskqueue.EnqueueInput{
		SourceMessageKey: "source-preflight-cancel", OwnerID: "owner", ProjectID: "project",
		Summary: "取消测试", Text: "执行", ResponseMode: preference.ResponseAdaptive, VisualStyle: visual.StyleEditorial,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimNext(nil)
	if err != nil || !ok || claimed.ID != queued.ID {
		t.Fatalf("ClaimNext() = %#v, %v, %v", claimed, ok, err)
	}
	coordinator, err := NewCoordinator(handler, store)
	if err != nil {
		t.Fatal(err)
	}
	active := &coordinatorTask{ownerID: "owner", taskID: claimed.ID, cancelRequested: true}
	coordinator.active = active
	coordinator.failBeforeExecution(nil, claimed, active, "不应发送", taskqueue.ReasonPayloadInvalid, nil)
	finished, exists := store.Find("owner", claimed.ID)
	if !exists || finished.State != taskqueue.StateCancelled || finished.Reason != taskqueue.ReasonUserCancelled {
		t.Fatalf("cancelled task = %#v, exists=%v", finished, exists)
	}
}
