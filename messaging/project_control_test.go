package messaging

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huixiangyang/weclaw/config"
	"github.com/huixiangyang/weclaw/project"
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
	if !handled || !strings.Contains(menu, "审查改动") {
		t.Fatalf("quick task menu = %q, %v", menu, handled)
	}
	reply, handled := handler.handleControlInput(context.Background(), "owner-1", "1", false)
	if !handled || reply != "" {
		t.Fatalf("quick task action = %q, %v", reply, handled)
	}
	if prompt, ok := handler.controlDispatches.LoadAndDelete("owner-1"); !ok || prompt != "审查当前项目改动" {
		t.Fatalf("quick task dispatch = %#v, %v", prompt, ok)
	}
}

func TestPendingInstructionKeepsOnlyOneFollowUp(t *testing.T) {
	handler, _ := newSessionHandler(t)
	task := newActiveTask(context.Background())
	handler.activeTasks.Store("owner-1", task)
	defer func() { task.finish(); handler.activeTasks.Delete("owner-1") }()
	if reply := handler.queuePendingInstruction("owner-1", "继续跑全量测试"); !strings.Contains(reply, "已暂存") {
		t.Fatalf("queue reply = %q", reply)
	}
	if reply := handler.queuePendingInstruction("owner-1", "覆盖原指令"); !strings.Contains(reply, "已经暂存") {
		t.Fatalf("second queue reply = %q", reply)
	}
	main := handler.openMainMenu(context.Background(), "owner-1")
	for _, want := range []string{"1  任务状态", "2  暂存下一条指令", "3  当前会话", "4  更多功能"} {
		if !strings.Contains(main, want) {
			t.Fatalf("active menu missing %q: %q", want, main)
		}
	}
	if reply := handler.clearPendingInstruction("owner-1"); !strings.Contains(reply, "已清除") {
		t.Fatalf("clear reply = %q", reply)
	}
}
