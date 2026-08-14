package bridge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/workspace"
)

func TestProjectSelectionIsolatesSessions(t *testing.T) {
	handler, _ := newSessionHandler(t)
	projects, err := workspace.NewManager([]workspace.Definition{
		{ID: "alpha", Name: "Alpha", Root: t.TempDir()},
		{ID: "beta", Name: "Beta", Root: t.TempDir()},
	}, filepath.Join(t.TempDir(), "project-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	handler.projects = projects
	created := controlReply(t, handler, "owner-1", "新建线程 Alpha 线程")
	if !strings.Contains(created, "Alpha 线程") {
		t.Fatalf("alpha session = %q", created)
	}
	switched := controlReply(t, handler, "owner-1", "切换项目 beta")
	if !strings.Contains(switched, "当前：Beta") || projects.Current("owner-1").ID != "beta" {
		t.Fatalf("project switch = %q current=%q", switched, projects.Current("owner-1").ID)
	}
	if stats := handler.sessions.Stats("owner-1"); stats.Active != 0 || stats.HasCurrent {
		t.Fatalf("beta sessions leaked alpha state: %#v", stats)
	}
}

func TestProjectEntryAndCodexCapabilityViewsHaveSeparateOwnership(t *testing.T) {
	handler, _ := newSessionHandler(t)
	projects, err := workspace.NewManager([]workspace.Definition{{
		ID: "workspace", Name: "Workspace", Root: t.TempDir(),
	}}, filepath.Join(t.TempDir(), "project-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	handler.projects = projects
	entryView := handler.openProjectCenter(context.Background(), "owner-1")
	if !strings.HasPrefix(entryView, "Codex 工作空间\n") || strings.Contains(entryView, "去重技能：") || strings.Contains(entryView, "外部工具连接：") {
		t.Fatalf("project entry view mixed Codex capabilities: %q", entryView)
	}
	capabilityView := handler.openCodexCapabilities(context.Background(), "owner-1")
	for _, want := range []string{"Codex 全局技能与工具", "来源：Codex 应用服务", "去重技能：1 个启用", "外部工具连接：1 / 1 就绪"} {
		if !strings.Contains(capabilityView, want) {
			t.Fatalf("Codex capability view missing %q: %q", want, capabilityView)
		}
	}
}

func TestPromptTemplatePhrasesAreNoLongerControlIntents(t *testing.T) {
	registry := mustDefaultIntentRegistry()
	for _, phrase := range []string{"提示词模板", "提示模板", "新建提示词模板", "保存为提示词模板"} {
		if resolved, exists := registry.Resolve(phrase); exists {
			t.Fatalf("removed prompt template phrase %q resolved as %s", phrase, resolved.Definition.ID)
		}
	}
}

func TestProjectWatchPhrasesAreNoLongerControlIntents(t *testing.T) {
	registry := mustDefaultIntentRegistry()
	for _, phrase := range []string{"项目关注", "关注项目", "关注检查"} {
		if resolved, exists := registry.Resolve(phrase); exists {
			t.Fatalf("removed project watch phrase %q resolved as %s", phrase, resolved.Definition.ID)
		}
	}
}

func TestRunningMenuUsesPersistentTaskCenter(t *testing.T) {
	handler, cancel := testHandlerWithRunningTask(t, "owner-1")
	defer cancel()
	main := handler.openMainMenu(context.Background(), "owner-1")
	for _, want := range []string{"Codex 全局工作台", "微信队列：1 执行", "6  新建线程 · /new", "7  执行与队列", "8  工作空间", "Codex 功能"} {
		if !strings.Contains(main, want) {
			t.Fatalf("active menu missing %q: %q", want, main)
		}
	}
	state, status, err := handler.controlStates.Load("owner-1")
	if err != nil || status != controlStateActive || len(state.Options) != 20 {
		t.Fatalf("persistent workbench = %#v status=%v err=%v", state, status, err)
	}
}
