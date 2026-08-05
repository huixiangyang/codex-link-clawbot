package messaging

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huixiangyang/weclaw/config"
	"github.com/huixiangyang/weclaw/preference"
	"github.com/huixiangyang/weclaw/project"
	"github.com/huixiangyang/weclaw/taskqueue"
	"github.com/huixiangyang/weclaw/visual"
)

func TestProjectSelectionIsolatesSessionsAndRunsQuickTask(t *testing.T) {
	handler, _ := newSessionHandler(t)
	projects, err := project.NewManager([]config.ProjectConfig{
		{ID: "alpha", Name: "Alpha", Root: t.TempDir()},
		{ID: "beta", Name: "Beta", Root: t.TempDir(), QuickTasks: []config.QuickTaskConfig{{ID: "review", Name: "审查改动", Prompt: "审查当前项目改动"}}},
	}, filepath.Join(t.TempDir(), "project-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	handler.SetProjectManager(projects)
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
	menu, handled := handler.handleControlInput(context.Background(), "owner-1", "快捷任务", false)
	if !handled || !strings.Contains(menu.Text, "审查改动") {
		t.Fatalf("quick task menu = %q, %v", menu.Text, handled)
	}
	reply, handled := handler.handleControlInput(context.Background(), "owner-1", "1", false)
	if !handled || reply.Text != "" || reply.Effect.Kind != EffectEnqueuePrompt || reply.Effect.Value != "审查当前项目改动" {
		t.Fatalf("quick task action = %#v, %v", reply, handled)
	}
}

func TestProjectSelectionAndQuickTaskRemainAvailableWhileAnotherTaskRuns(t *testing.T) {
	handler, runtime := newSessionHandler(t)
	projects, err := project.NewManager([]config.ProjectConfig{
		{ID: "alpha", Name: "Alpha", Root: t.TempDir()},
		{ID: "beta", Name: "Beta", Root: t.TempDir(), QuickTasks: []config.QuickTaskConfig{{ID: "review", Name: "审查改动", Prompt: "审查 Beta"}}},
	}, filepath.Join(t.TempDir(), "project-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	handler.SetProjectManager(projects)
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
	if reply := handler.runProjectQuickTask("owner-1", "review"); reply.Text != "" || reply.Effect.Kind != EffectEnqueuePrompt || reply.Effect.Value != "审查 Beta" {
		t.Fatalf("quick task while running = %#v", reply)
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
