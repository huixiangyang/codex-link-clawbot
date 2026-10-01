package wechat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/execution"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/workspace"
)

type finalReplyOrderAgent struct {
	*handlerThreadClient
	called chan struct{}
	once   sync.Once
}

type structuredPhaseAgent struct {
	*handlerThreadClient
}

func (agent *structuredPhaseAgent) ChatThread(_ context.Context, _ string, _ codex.ChatRequest) (string, error) {
	return "不应走无阶段接口", nil
}

func (agent *structuredPhaseAgent) ChatThreadWithProgress(_ context.Context, _ string, _ codex.ChatRequest, onPhase codex.TurnPhaseHandler) (string, error) {
	events := []codex.TurnPhaseEvent{
		{TurnID: "turn-phase", Phase: codex.TurnPhaseStarted},
		{TurnID: "turn-phase", Phase: codex.TurnPhaseStarted},
		{TurnID: "turn-phase", Phase: codex.TurnPhasePlanning, Step: "实现阶段状态机", Complete: 1, Total: 2},
		{TurnID: "turn-phase", Phase: codex.TurnPhasePlanning, Step: "实现阶段状态机", Complete: 1, Total: 2},
		{TurnID: "turn-phase", Phase: codex.TurnPhaseCompleted},
		{TurnID: "turn-phase", Phase: codex.TurnPhaseWorking},
	}
	for _, event := range events {
		onPhase(event)
	}
	return "执行完成", nil
}

func (agent *finalReplyOrderAgent) ChatThread(_ context.Context, _ string, _ codex.ChatRequest) (string, error) {
	agent.once.Do(func() { close(agent.called) })
	return "执行完成", nil
}

func TestCoordinatorFastWorkOnlySendsFinalReply(t *testing.T) {
	replyStarted := make(chan struct{})
	replyRelease := make(chan struct{})
	var sendCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			http.NotFound(w, r)
			return
		}
		if sendCount.Add(1) == 1 {
			close(replyStarted)
			<-replyRelease
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	client := ilink.NewClient(&ilink.Credentials{
		BotToken: "token", ILinkBotID: "bot", ILinkUserID: "owner", BaseURL: server.URL,
	})
	agent := &finalReplyOrderAgent{handlerThreadClient: newHandlerThreadClient(), called: make(chan struct{})}
	handler := newBareHandler(agent)
	attachTestSessionManager(t, handler)
	handler.progress = execution.ProgressConfig{Enabled: false}
	store, stop := attachTestExecution(t, handler, client, "owner")
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
	case <-replyStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("final reply did not start")
	}
	select {
	case <-agent.called:
	default:
		close(replyRelease)
		t.Fatal("reply sent before Codex executed")
	}
	close(replyRelease)
	if err := <-handleErr; err != nil {
		t.Fatal(err)
	}
	waitForTerminalTask(t, store, "owner")
	if sendCount.Load() != 1 {
		t.Fatalf("fast work sent %d messages, want final reply only", sendCount.Load())
	}
}

func TestCoordinatorPersistsOnlyDistinctStructuredTurnPhases(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot", ILinkUserID: "owner", BaseURL: server.URL})
	handler := newBareHandler(&structuredPhaseAgent{handlerThreadClient: newHandlerThreadClient()})
	attachTestSessionManager(t, handler)
	handler.progress = execution.ProgressConfig{Enabled: false}
	store, stop := attachTestExecution(t, handler, client, "owner")
	defer stop()

	if err := handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
		MessageID: 93, FromUserID: "owner", ToUserID: "bot", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish, ContextToken: "context",
		ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "执行阶段测试"}}},
	}); err != nil {
		t.Fatal(err)
	}
	terminal := waitForTerminalTask(t, store, "owner")
	if terminal.State != request.StateSucceeded || terminal.Stage != "已完成" {
		t.Fatalf("terminal task = %#v", terminal)
	}
}

func TestExecutionKeepsProjectSessionAndPreferenceSnapshot(t *testing.T) {
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
	handler := newBareHandler(runtime)
	projects, err := workspace.NewManager([]workspace.Definition{
		{ID: "alpha", Name: "Alpha", Root: t.TempDir()},
		{ID: "beta", Name: "Beta", Root: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler.projects = projects
	attachTestSessionManager(t, handler)
	preferences, err := preference.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := preferences.SetStyle("owner", presentation.StyleNoir); err != nil {
		t.Fatal(err)
	}
	handler.preferences = preferences
	store, err := request.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	coordinator, err := newTestCoordinator(handler, store)
	if err != nil {
		t.Fatal(err)
	}
	handler.tasks = store
	handler.coordinator = coordinator
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = coordinator.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	definition, _ := projects.Get("alpha")
	original, err := handler.sessions.OpenTaskThread(context.Background(), "owner", definition, "", runtime, "冻结线程")
	if err != nil {
		t.Fatal(err)
	}
	originalThread := original.ID
	if _, err := handler.targets.SelectThread("owner", "alpha", originalThread, handler.targets.Snapshot("owner")); err != nil {
		t.Fatal(err)
	}
	message := ilink.WeixinMessage{
		MessageID: 91, FromUserID: "owner", ToUserID: "bot",
		MessageType: ilink.MessageTypeUser, MessageState: ilink.MessageStateFinish, ContextToken: "context",
		ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "检查 Alpha"}}},
	}
	if err := handler.HandleMessage(context.Background(), client, message); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.targets.SelectWorkspace("owner", "beta"); err != nil {
		t.Fatal(err)
	}
	if err := preferences.SetStyle("owner", presentation.StyleCute); err != nil {
		t.Fatal(err)
	}
	if err := preferences.SetResponseMode("owner", presentation.ResponseVoice); err != nil {
		t.Fatal(err)
	}
	tasks := store.List("owner")
	if len(tasks) != 1 {
		t.Fatalf("active requests = %#v", tasks)
	}
	task := tasks[0]
	if task.ProjectID != "alpha" || task.ThreadID != originalThread || task.ResponseMode != presentation.ResponseText || task.VisualStyle != presentation.StyleNoir {
		t.Fatalf("task snapshot changed after UI selection: %#v", task)
	}
}

type continuousThreadAgent struct {
	*handlerThreadClient
	executed chan string
}

func (a *continuousThreadAgent) ChatThread(_ context.Context, id string, _ codex.ChatRequest) (string, error) {
	a.executed <- id
	return "同一会话继续", nil
}
func TestCompletedMessagesContinueOneThread(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"ret":0}`)) }))
	defer server.Close()
	client := ilink.NewClient(&ilink.Credentials{BotToken: "fixture", ILinkBotID: "bot", ILinkUserID: "owner", BaseURL: server.URL})
	agent := &continuousThreadAgent{handlerThreadClient: newHandlerThreadClient(), executed: make(chan string, 2)}
	handler := newBareHandler(agent)
	attachTestSessionManager(t, handler)
	handler.progress = execution.ProgressConfig{Enabled: false}
	store, stop := attachTestExecution(t, handler, client, "owner")
	defer stop()

	for i := int64(1); i <= 2; i++ {
		if err := handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{MessageID: i, FromUserID: "owner", ToUserID: "bot", MessageType: ilink.MessageTypeUser, MessageState: ilink.MessageStateFinish, ContextToken: "context", ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "继续修复"}}}}); err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(3 * time.Second)
		for store.Status("owner").Succeeded < int(i) && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		if store.Status("owner").Succeeded != int(i) {
			t.Fatal("previous execution did not finish")
		}
		// 完成状态写盘与释放准入之间有极短窗口，以新消息前的实际空闲为准。
		time.Sleep(10 * time.Millisecond)
	}
	ids := []string{}
	for len(ids) < 2 {
		select {
		case id := <-agent.executed:
			ids = append(ids, id)
		case <-time.After(3 * time.Second):
			t.Fatal("continuous requests did not execute")
		}
	}
	if ids[0] != ids[1] {
		t.Fatalf("first messages created independent threads: %v", ids)
	}
	if target := handler.targets.Current("owner"); target.ThreadID != ids[0] {
		t.Fatalf("current thread not resolved: %+v", target)
	}
	if _, err := handler.clients.context("owner"); err != nil {
		t.Fatal(err)
	}
	handler.clients.contexts.Store("owner", liveContext{token: "stale", received: time.Now().Add(-25 * time.Hour)})
	(&Runtime{Handler: handler.Handler}).ExpireMessageContexts()
	if _, found := handler.clients.contexts.Load("owner"); found {
		t.Fatal("expired context retained")
	}
	if _, err := handler.clients.context("owner"); err == nil {
		t.Fatal("recovery used expired context")
	}
}
