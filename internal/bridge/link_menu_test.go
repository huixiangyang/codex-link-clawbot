package bridge

import (
	"strings"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/workspace"
)

func TestCodexLinkMenuIsTheOnlyWeChatControlEntry(t *testing.T) {
	for _, input := range []string{"菜单", "Codex", "codex 菜单", "Codex Link"} {
		if !isCodexLinkMenu(input) {
			t.Fatalf("%q should open Codex Link", input)
		}
	}
	for _, input := range []string{"1", "状态", "取消", "请求队列", "视觉风格", "文字版"} {
		if isCodexLinkMenu(input) {
			t.Fatalf("legacy command %q must be treated as a Codex prompt", input)
		}
	}
}

func TestCodexLinkMenuPointsToManagementConsole(t *testing.T) {
	projects, err := workspace.NewManager([]workspace.Definition{{ID: "workspace", Name: "主工作区", Root: t.TempDir()}}, "")
	if err != nil {
		t.Fatal(err)
	}
	handler := &Handler{projects: projects, managementURL: "https://link.example.com"}
	menu := handler.codexLinkMenu("owner", false)
	for _, want := range []string{"Codex Link", "连接状态：已连接", "工作空间：主工作区", "https://link.example.com", "请在管理页面处理"} {
		if !strings.Contains(menu, want) {
			t.Fatalf("menu missing %q: %q", want, menu)
		}
	}
}
