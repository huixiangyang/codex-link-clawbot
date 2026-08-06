package messaging

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huixiangyang/weclaw/internal/config"
	"github.com/huixiangyang/weclaw/internal/ilink"
	"github.com/huixiangyang/weclaw/internal/preference"
	"github.com/huixiangyang/weclaw/internal/project"
	"github.com/huixiangyang/weclaw/internal/taskqueue"
	"github.com/huixiangyang/weclaw/internal/visual"
	"github.com/huixiangyang/weclaw/internal/workflow"
)

type taskReuseTestEnvironment struct {
	handler      *Handler
	task         taskqueue.Task
	threadID     string
	prompt       string
	workflowPath string
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
	projects, err := project.NewManager([]config.ProjectConfig{
		{ID: "alpha", Name: "Alpha", Root: alphaRoot},
		{ID: "beta", Name: "Beta", Root: betaRoot},
	}, filepath.Join(root, "project-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	handler.SetProjectManager(projects)
	if reply := handler.createSession(context.Background(), "owner-1", "发布线程"); !strings.Contains(reply, "已创建") {
		t.Fatalf("create task session = %q", reply)
	}
	threadID := handler.sessions.SnapshotThreadID("owner-1", "alpha")
	if threadID == "" {
		t.Fatal("task session was not registered")
	}
	store, err := taskqueue.NewStore(filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	prompt := "检查发布分支并整理风险"
	task, _, err := store.Enqueue(taskqueue.EnqueueInput{
		SourceMessageKey: "source:successful", OwnerID: "owner-1", ProjectID: "alpha", ThreadID: threadID,
		Summary: "发布风险检查", Text: prompt, ContextToken: "不得保存的令牌",
		ResponseMode: preference.ResponseAdaptive, VisualStyle: visual.StyleEditorial,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.ClaimNext(nil); err != nil || !claimed {
		t.Fatalf("ClaimNext() claimed=%v err=%v", claimed, err)
	}
	if _, err := store.FreezeResult("owner-1", task.ID, taskqueue.FreezeResultInput{Reply: "不得保存的回答"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginDelivery("owner-1", task.ID); err != nil {
		t.Fatal(err)
	}
	finished, err := store.Finish("owner-1", task.ID, taskqueue.StateSucceeded, "")
	if err != nil {
		t.Fatal(err)
	}
	handler.tasks = store
	workflowPath := filepath.Join(root, "workflows.json")
	workflows, err := workflow.NewStore(workflowPath, []string{"alpha", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	handler.SetWorkflowStore(workflows)
	return taskReuseTestEnvironment{
		handler: handler, task: finished, threadID: threadID, prompt: prompt, workflowPath: workflowPath,
	}
}

func TestSuccessfulTaskOffersContinueRerunNewSessionAndWorkflowSave(t *testing.T) {
	environment := newTaskReuseTestEnvironment(t)
	handler := environment.handler
	detail := handler.openActivityDetail("owner-1", environment.task.ID, 1)
	for _, want := range []string{"继续这个线程", "再次执行", "在新线程执行", "保存为提示词模板"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("task detail missing %q: %q", want, detail)
		}
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

	_ = handler.openActivityDetail("owner-1", environment.task.ID, 1)
	savePrompt := controlReply(t, handler, "owner-1", "4")
	if !strings.Contains(savePrompt, "保存的是原始请求") {
		t.Fatalf("workflow save prompt = %q", savePrompt)
	}
	saveSource := nextTestControlSource()
	saved, handled := handler.handleControlInput(
		context.Background(), "owner-1", "发布检查", false, saveSource,
	)
	if !handled || !strings.Contains(saved.Text, "已从成功请求保存") {
		t.Fatalf("saved workflow = %#v, handled=%v", saved, handled)
	}
	definitions := handler.workflows.List("owner-1", "alpha")
	if len(definitions) != 1 || definitions[0].Name != "发布检查" || definitions[0].PromptTemplate != environment.prompt || len(definitions[0].Slots) != 0 {
		t.Fatalf("saved workflow definition = %#v", definitions)
	}
	duplicate, handled := handler.handleControlInput(
		context.Background(), "owner-1", "发布检查", false, saveSource,
	)
	if !handled || !strings.Contains(duplicate.Text, "不会重复执行") || len(handler.workflows.List("owner-1", "alpha")) != 1 {
		t.Fatalf("duplicate workflow save = %#v, handled=%v", duplicate, handled)
	}
	raw, err := os.ReadFile(environment.workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"不得保存的回答", "不得保存的令牌"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("workflow state leaked %q: %s", forbidden, raw)
		}
	}
	directPrompt := controlReply(t, handler, "owner-1", "保存为提示词模板")
	if !strings.Contains(directPrompt, "发送提示词模板名称") {
		t.Fatalf("direct workflow save prompt = %q", directPrompt)
	}
	_ = controlReply(t, handler, "owner-1", "0")
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
	if _, err := handler.workflows.Create(workflow.CreateInput{
		OwnerID: "owner-1", ProjectID: "alpha", Name: "发布检查", PromptTemplate: "检查发布", Slots: []workflow.Slot{},
	}); err != nil {
		t.Fatal(err)
	}
	main := handler.openMainMenu(context.Background(), "owner-1")
	for _, want := range []string{"56  提示词模板 · WeClaw · 1 项", "57  保存最近结果", "32  最近执行结果", "[2]  Codex · 执行能力"} {
		if !strings.Contains(main, want) {
			t.Fatalf("stable directory missing %q: %q", want, main)
		}
	}
	state, status, err := handler.controlStates.Load("owner-1")
	if err != nil || status != controlStateActive || state == nil || len(state.Options) != 44 {
		t.Fatalf("stable directory options = %#v status=%v err=%v", state, status, err)
	}
	workflowOption, hasWorkflowCode := controlOptionByCode("56", state.Options)
	if !hasWorkflowCode || workflowOption.Action != actionProjectQuickTasks || workflowOption.AutoUse {
		t.Fatalf("stable directory options = %#v status=%v err=%v", state, status, err)
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
	handler.SetPreferenceStore(preferences)
	coordinator, err := NewCoordinator(handler, handler.tasks)
	if err != nil {
		t.Fatal(err)
	}
	handler.SetTaskQueue(handler.tasks, coordinator)
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
