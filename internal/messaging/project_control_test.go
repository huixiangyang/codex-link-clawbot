package messaging

import (
	"context"
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

func attachProjectWorkflow(t *testing.T, handler *Handler, ownerID, projectID, name, prompt string) workflow.Definition {
	t.Helper()
	store, err := workflow.NewStore(filepath.Join(t.TempDir(), "workflows.json"), []string{"alpha", "beta"})
	if err != nil {
		t.Fatal(err)
	}
	definition, err := store.Create(workflow.CreateInput{
		OwnerID: ownerID, ProjectID: projectID, Name: name, PromptTemplate: prompt, Slots: []workflow.Slot{},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler.SetWorkflowStore(store)
	return definition
}

func TestProjectSelectionIsolatesSessionsAndRunsQuickTask(t *testing.T) {
	handler, _ := newSessionHandler(t)
	projects, err := project.NewManager([]config.ProjectConfig{
		{ID: "alpha", Name: "Alpha", Root: t.TempDir()},
		{ID: "beta", Name: "Beta", Root: t.TempDir()},
	}, filepath.Join(t.TempDir(), "project-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	handler.SetProjectManager(projects)
	attachProjectWorkflow(t, handler, "owner-1", "beta", "审查改动", "审查当前项目改动")
	created := controlReply(t, handler, "owner-1", "新建会话 Alpha 会话")
	if !strings.Contains(created, "Alpha 会话") {
		t.Fatalf("alpha session = %q", created)
	}
	switched := controlReply(t, handler, "owner-1", "切换项目 beta")
	if !strings.Contains(switched, "当前：Beta") || projects.Current("owner-1").ID != "beta" {
		t.Fatalf("project switch = %q current=%q", switched, projects.Current("owner-1").ID)
	}
	if stats := handler.sessions.Stats("owner-1"); stats.Active != 0 || stats.HasCurrent {
		t.Fatalf("beta sessions leaked alpha state: %#v", stats)
	}
	menu, handled := handler.handleControlInput(context.Background(), "owner-1", "快捷任务", false, nextTestControlSource())
	if !handled || !strings.Contains(menu.Text, "审查改动") {
		t.Fatalf("quick task menu = %q, %v", menu.Text, handled)
	}
	detail, handled := handler.handleControlInput(context.Background(), "owner-1", "2", false, nextTestControlSource())
	if !handled || !strings.Contains(detail.Text, "快捷任务详情") {
		t.Fatalf("quick task detail = %#v, %v", detail, handled)
	}
	quickTaskSource := nextTestControlSource()
	reply, handled := handler.handleControlInput(context.Background(), "owner-1", "1", false, quickTaskSource)
	if !handled || reply.Text != "" || reply.Effect.Kind != EffectEnqueuePrompt || reply.Effect.Value != "审查当前项目改动" {
		t.Fatalf("quick task action = %#v, %v", reply, handled)
	}
	duplicate, handled := handler.handleControlInput(context.Background(), "owner-1", "1", false, quickTaskSource)
	if !handled || duplicate.Effect.Kind != EffectNone || !strings.Contains(duplicate.Text, "不会重复执行") {
		t.Fatalf("duplicate quick task action = %#v, %v", duplicate, handled)
	}
}

func TestProjectSelectionAndQuickTaskRemainAvailableWhileAnotherTaskRuns(t *testing.T) {
	handler, runtime := newSessionHandler(t)
	projects, err := project.NewManager([]config.ProjectConfig{
		{ID: "alpha", Name: "Alpha", Root: t.TempDir()},
		{ID: "beta", Name: "Beta", Root: t.TempDir()},
	}, filepath.Join(t.TempDir(), "project-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	handler.SetProjectManager(projects)
	definition := attachProjectWorkflow(t, handler, "owner-1", "beta", "审查改动", "审查 Beta")
	store, err := taskqueue.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.Enqueue(taskqueue.EnqueueInput{
		SourceMessageKey: "source-running-project", OwnerID: "owner-1", ProjectID: "alpha", Summary: "运行中", Text: "执行",
		ResponseMode: preference.ResponseAdaptive, VisualStyle: visual.StyleEditorial,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.ClaimNext(nil); err != nil || !claimed {
		t.Fatalf("ClaimNext() claimed=%v err=%v", claimed, err)
	}
	handler.tasks = store

	switched := handler.selectProject("owner-1", "beta")
	if !strings.Contains(switched, "当前：Beta") || projects.Current("owner-1").ID != "beta" {
		t.Fatalf("project switch while running = %q", switched)
	}
	if len(runtime.cwdChanges) != 0 {
		t.Fatalf("UI project selection mutated Codex cwd: %#v", runtime.cwdChanges)
	}
	menu := handler.openProjectQuickTasks("owner-1")
	if !strings.Contains(menu, "审查改动") {
		t.Fatalf("quick tasks while running = %q", menu)
	}
	if reply := handler.runProjectQuickTask("owner-1", "beta", definition.ID); reply.Text != "" || reply.Effect.Kind != EffectEnqueuePrompt || reply.Effect.Value != "审查 Beta" || reply.Effect.ProjectID != "beta" {
		t.Fatalf("quick task while running = %#v", reply)
	}
}

func TestQuickTaskQueueFailureRestoresMenuRevision(t *testing.T) {
	handler, _ := newSessionHandler(t)
	projects, err := project.NewManager([]config.ProjectConfig{{
		ID: "alpha", Name: "Alpha", Root: t.TempDir(),
	}}, filepath.Join(t.TempDir(), "project-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	handler.SetProjectManager(projects)
	attachProjectWorkflow(t, handler, "owner-1", "alpha", "审查改动", "审查当前项目改动")
	_ = controlReply(t, handler, "owner-1", "快捷任务")
	_ = controlReply(t, handler, "owner-1", "2")
	before, status, err := handler.controlStates.Load("owner-1")
	if err != nil || status != controlStateActive {
		t.Fatalf("quick task menu = %#v, status=%v err=%v", before, status, err)
	}
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1"})
	message := ilink.WeixinMessage{
		MessageID: 8801, FromUserID: "owner-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish, ContextToken: "context",
		ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "1"}}},
	}
	if err := handler.HandleMessage(context.Background(), client, message); err == nil || !strings.Contains(err.Error(), "task queue is not initialized") {
		t.Fatalf("HandleMessage() error = %v", err)
	}
	after, status, err := handler.controlStates.Load("owner-1")
	if err != nil || status != controlStateActive || after.Revision != before.Revision {
		t.Fatalf("restored quick task menu = %#v, status=%v err=%v", after, status, err)
	}
	sourceKey, err := sourceMessageKey(client, message)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := handler.controlStates.FindReceipt("owner-1", sourceKey); exists {
		t.Fatal("failed quick task kept its control receipt")
	}
}

func TestRunningMenuUsesPersistentTaskCenter(t *testing.T) {
	handler, cancel := testHandlerWithRunningTask(t, "owner-1")
	defer cancel()
	main := handler.openMainMenu(context.Background(), "owner-1")
	for _, want := range []string{"1  任务状态", "2  任务中心", "3  当前会话", "4  更多功能"} {
		if !strings.Contains(main, want) {
			t.Fatalf("active menu missing %q: %q", want, main)
		}
	}
}
