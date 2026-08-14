package bridge

import (
	"context"
	"encoding/json"
	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/workspace"
)

type taskReuseTestEnvironment struct {
	handler  *Handler
	task     request.Task
	threadID string
	prompt   string
}

func newTaskReuseTestEnvironment(t *testing.T) taskReuseTestEnvironment {
	t.Helper()
	handler, _ := newSessionHandler(t)
	root := t.TempDir()
	alphaRoot := filepath.Join(root, "alpha")
	betaRoot := filepath.Join(root, "beta")
	if err := os.Mkdir(alphaRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(betaRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	projects, err := workspace.NewManager([]workspace.Definition{
		{ID: "alpha", Name: "Alpha", Root: alphaRoot},
		{ID: "beta", Name: "Beta", Root: betaRoot},
	}, filepath.Join(root, "project-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	handler.projects = projects
	if reply := handler.createSession(context.Background(), "owner-1", "发布线程"); !strings.Contains(reply, "已创建") {
		t.Fatalf("create task session = %q", reply)
	}
	threadID := handler.sessions.SnapshotThreadID("owner-1", "alpha")
	if threadID == "" {
		t.Fatal("task session was not registered")
	}
	store, err := request.NewStore(filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	prompt := "检查发布分支并整理风险"
	task, _, err := store.Enqueue(request.EnqueueInput{
		SourceMessageKey: "source:successful", OwnerID: "owner-1", ProjectID: "alpha", ThreadID: threadID,
		Summary: "发布风险检查", Text: prompt, ContextToken: "不得保存的令牌",
		ResponseMode: presentation.ResponseAdaptive, VisualStyle: presentation.StyleEditorial,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.ClaimNext(nil); err != nil || !claimed {
		t.Fatalf("ClaimNext() claimed=%v err=%v", claimed, err)
	}
	if _, err := store.FreezeResult("owner-1", task.ID, request.FreezeResultInput{Reply: "不得保存的回答"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginDelivery("owner-1", task.ID); err != nil {
		t.Fatal(err)
	}
	finished, err := store.Finish("owner-1", task.ID, request.StateSucceeded, "")
	if err != nil {
		t.Fatal(err)
	}
	handler.tasks = store
	return taskReuseTestEnvironment{
		handler: handler, task: finished, threadID: threadID, prompt: prompt,
	}
}

func TestSuccessfulTaskOffersOnlyContinueAndRerunActions(t *testing.T) {
	environment := newTaskReuseTestEnvironment(t)
	handler := environment.handler
	detail := handler.openActivityDetail("owner-1", environment.task.ID, 1)
	for _, want := range []string{"继续这个线程", "再次执行", "在新线程执行"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("task detail missing %q: %q", want, detail)
		}
	}
	if strings.Contains(detail, "提示词模板") {
		t.Fatalf("removed prompt template action survived: %q", detail)
	}

	rerun := handler.requestSuccessfulTaskRerun("owner-1", environment.task.ID, false)
	if rerun.Effect.Kind != EffectEnqueuePrompt || rerun.Effect.Value != environment.prompt ||
		rerun.Effect.ProjectID != "alpha" || rerun.Effect.ThreadID != environment.threadID || rerun.Effect.NewThread {
		t.Fatalf("same-session rerun = %#v", rerun)
	}
	newSession := handler.requestSuccessfulTaskRerun("owner-1", environment.task.ID, true)
	if newSession.Effect.Kind != EffectEnqueuePrompt || newSession.Effect.ProjectID != "alpha" ||
		newSession.Effect.ThreadID != "" || !newSession.Effect.NewThread {
		t.Fatalf("new-session rerun = %#v", newSession)
	}

}

func TestTaskContinuationSwitchesBackToFrozenProjectAndUpdatesStableDirectory(t *testing.T) {
	environment := newTaskReuseTestEnvironment(t)
	handler := environment.handler
	if _, err := handler.projects.Select("owner-1", "beta"); err != nil {
		t.Fatal(err)
	}
	reply := handler.continueTaskSession(context.Background(), "owner-1", environment.task.ID, 1)
	if !strings.Contains(reply, "已回到执行记录关联的 Codex 线程") || handler.projects.Current("owner-1").ID != "alpha" ||
		handler.sessions.SnapshotThreadID("owner-1", "alpha") != environment.threadID {
		t.Fatalf("continued task session = %q current=%q", reply, handler.projects.Current("owner-1").ID)
	}
	main := handler.buildCommandDirectory(context.Background(), "owner-1").Text
	for _, want := range []string{"12  全局线程", "32  审查改动", "42  系统健康与诊断", "43  呈现与安全", "[3]  Codex · 执行", "[4]  codex-link-clawbot · 远程"} {
		if !strings.Contains(main, want) {
			t.Fatalf("stable directory missing %q: %q", want, main)
		}
	}
	state, status, err := handler.controlStates.Load("owner-1")
	if err != nil || status != controlStateActive || state == nil || len(state.Options) != 19 {
		t.Fatalf("stable directory options = %#v status=%v err=%v", state, status, err)
	}
	diagnosticsOption, hasDiagnosticsCode := controlOptionByCode("42", state.Options)
	if !hasDiagnosticsCode || diagnosticsOption.Action != actionDiagnosticsCenter {
		t.Fatalf("stable directory options = %#v status=%v err=%v", state, status, err)
	}
	projectCenter := controlReply(t, handler, "owner-1", "菜单")
	_ = projectCenter
	_ = controlReply(t, handler, "owner-1", "8")
	projectCenter = controlReply(t, handler, "owner-1", "21")
	if strings.Contains(projectCenter, "提示词模板") || strings.Contains(projectCenter, "项目关注") {
		t.Fatalf("project center retained removed features: %q", projectCenter)
	}
	projectState, projectStatus, projectErr := handler.controlStates.Load("owner-1")
	if projectErr != nil || projectStatus != controlStateActive || len(projectState.Options) != 2 ||
		projectState.Options[0].Action != actionSelectProject || projectState.Options[1].Action != actionSelectProject {
		t.Fatalf("project center options = %#v status=%v err=%v", projectState, projectStatus, projectErr)
	}
}

func TestDirectTaskRerunRollsBackReceiptWhenQueueUnavailable(t *testing.T) {
	environment := newTaskReuseTestEnvironment(t)
	handler := environment.handler
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1"})
	message := ilink.WeixinMessage{
		MessageID: 10001, FromUserID: "owner-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish, ContextToken: "context",
		ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "重试上次任务"}}},
	}
	if err := handler.HandleMessage(context.Background(), client, message); err == nil || !strings.Contains(err.Error(), "task queue is not initialized") {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	sourceKey, err := sourceMessageKey(client, message)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := handler.controlStates.FindReceipt("owner-1", sourceKey); exists {
		t.Fatal("failed direct rerun kept its receipt")
	}
}

func TestTaskRerunPresenterFreezesExistingOrNewThread(t *testing.T) {
	environment := newTaskReuseTestEnvironment(t)
	handler := environment.handler
	preferences, err := preference.NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	handler.preferences = preferences
	coordinator, err := newCoordinator(handler, handler.tasks)
	if err != nil {
		t.Fatal(err)
	}
	handler.coordinator = coordinator
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/ilink/bot/sendmessage" {
			http.NotFound(response, request)
			return
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"ret": 0})
	}))
	defer server.Close()
	client := ilink.NewClient(&ilink.Credentials{
		BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL,
	})

	for _, test := range []struct {
		messageID  int64
		newThread  bool
		wantThread string
	}{
		{messageID: 11001, wantThread: environment.threadID},
		{messageID: 11002, newThread: true, wantThread: ""},
	} {
		message := ilink.WeixinMessage{
			MessageID: test.messageID, FromUserID: "owner-1", ContextToken: "context",
		}
		result := handler.requestSuccessfulTaskRerun("owner-1", environment.task.ID, test.newThread)
		if err := handler.presentActionResult(context.Background(), client, message, result, NewClientID()); err != nil {
			t.Fatal(err)
		}
		sourceKey, err := sourceMessageKey(client, message)
		if err != nil {
			t.Fatal(err)
		}
		queued, exists := handler.tasks.FindBySource(sourceKey)
		if !exists || queued.ProjectID != "alpha" || queued.ThreadID != test.wantThread {
			t.Fatalf("queued rerun = %#v exists=%v", queued, exists)
		}
	}
}
