package messaging

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/config"
	"github.com/huixiangyang/codex-link-clawbot/internal/project"
)

func TestCodexSlashRegistryCoversOfficialCommandsAndAliases(t *testing.T) {
	if err := validateCodexSlashRegistry(); err != nil {
		t.Fatal(err)
	}
	if len(codexSlashCommands) != 49 {
		t.Fatalf("canonical slash commands = %d, want 49", len(codexSlashCommands))
	}
	if len(codexSlashByName) != 54 {
		t.Fatalf("slash commands including aliases = %d, want 54", len(codexSlashByName))
	}
	if got := codexSlashRemoteCommandCount(); got != 17 {
		t.Fatalf("remote usable slash commands = %d, want 17", got)
	}
	visible := make(map[string]bool, codexSlashRemoteCommandCount())
	for _, group := range codexSlashWorkbenchGroups() {
		for _, command := range group.Commands {
			if !command.Support.remoteUsable() || visible[command.Name] {
				t.Fatalf("invalid visible command = %#v", command)
			}
			visible[command.Name] = true
		}
	}
	for _, command := range codexSlashCommands {
		if visible[command.Name] != command.Support.remoteUsable() {
			t.Fatalf("visible state mismatch for /%s", command.Name)
		}
	}
	for name, command := range codexSlashByName {
		parsed, _, ok := parseCodexSlashCommand("/" + name)
		if !ok || parsed != command {
			t.Fatalf("parse /%s = %#v, %v", name, parsed, ok)
		}
	}
	command, argument, ok := parseCodexSlashCommand("/goal 完成菜单重构")
	if !ok || command.Name != "goal" || argument != "完成菜单重构" {
		t.Fatalf("goal command = %#v argument=%q ok=%v", command, argument, ok)
	}
}

func TestCodexSlashCommandsExecuteNativeMappings(t *testing.T) {
	handler, runtime := newSessionHandler(t)
	if handler.controlStates == nil {
		t.Fatal("newSessionHandler did not attach control state store")
	}
	projects, err := project.NewManager([]config.ProjectConfig{{
		ID: "workspace", Name: "Workspace", Root: t.TempDir(),
	}}, filepath.Join(t.TempDir(), "project-state.json"))
	if err != nil {
		t.Fatal(err)
	}
	handler.SetProjectManager(projects)
	created := controlReply(t, handler, "owner-1", "/new 命令线程")
	if !strings.Contains(created, "命令线程") || runtime.next != 1 {
		t.Fatalf("/new = %q next=%d", created, runtime.next)
	}
	status := controlReply(t, handler, "owner-1", "/status")
	if !strings.Contains(status, "当前线程") || !strings.Contains(status, "名称：命令线程") {
		t.Fatalf("/status = %q", status)
	}
	models := controlReply(t, handler, "owner-1", "/model")
	if !strings.Contains(models, "线程模型") || !strings.Contains(models, "快速模型") {
		t.Fatalf("/model = %q", models)
	}
	goal := controlReply(t, handler, "owner-1", "/goal 完成菜单重构")
	if !strings.Contains(goal, "线程目标已设置") {
		t.Fatalf("/goal = %q", goal)
	}
	paused := controlReply(t, handler, "owner-1", "/goal pause")
	if !strings.Contains(paused, "状态：已暂停") {
		t.Fatalf("/goal pause = %q", paused)
	}
	capabilities := controlReply(t, handler, "owner-1", "/mcp verbose")
	if !strings.Contains(capabilities, "外部工具连接：1 / 1 就绪") {
		t.Fatalf("/mcp = %q", capabilities)
	}
}

func TestCodexSlashCatalogOnlyRendersRemoteUsableCommands(t *testing.T) {
	handler, _ := newSessionHandler(t)
	center := handler.openCodexCommandCenter("owner-1")
	if !strings.Contains(center, "codex-link-clawbot 可用：17 个") || !strings.Contains(center, "线程与会话") || strings.Contains(center, "终端与账号") {
		t.Fatalf("command center = %q", center)
	}
	page := handler.openCodexCommandPage("owner-1", "thread", 1)
	if !strings.Contains(page, "清屏并新建线程 · /clear") || !strings.Contains(page, "重命名当前线程 · /rename") || strings.Contains(page, "/diff") {
		t.Fatalf("remote command page = %q", page)
	}
	terminal := handler.openCodexCommandPage("owner-1", "terminal", 1)
	if strings.Contains(terminal, "/keymap") || strings.Contains(terminal, "终端与账号") {
		t.Fatalf("terminal-only commands leaked into catalog = %q", terminal)
	}
}

func TestDirectCLIOnlyAndUnknownSlashCommandsStayOutOfCodex(t *testing.T) {
	handler, runtime := newSessionHandler(t)
	cliOnly := controlReply(t, handler, "owner-1", "/quit")
	if !strings.Contains(cliOnly, "退出 Codex CLI · /exit") || !strings.Contains(cliOnly, "常驻服务") {
		t.Fatalf("/quit = %q", cliOnly)
	}
	unknown, handled := handler.handleControlInput(context.Background(), "owner-1", "/sessions", false, nextTestControlSource())
	if !handled || !strings.Contains(unknown.Text, "没有这个可用的 Codex 斜杠命令") || runtime.chatThreadID != "" {
		t.Fatalf("unknown slash = %#v handled=%v thread=%q", unknown, handled, runtime.chatThreadID)
	}
}
