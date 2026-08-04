package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/weclaw/agent"
	"github.com/fastclaw-ai/weclaw/ilink"
)

func newTestHandler() *Handler {
	return &Handler{agents: make(map[string]agent.Agent)}
}

func TestParseCommand_NoPrefix(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("hello world")
	if len(names) != 0 {
		t.Errorf("expected nil names, got %v", names)
	}
	if msg != "hello world" {
		t.Errorf("expected full text, got %q", msg)
	}
}

func TestParseCommand_SlashWithAgent(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("/claude explain this code")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude], got %v", names)
	}
	if msg != "explain this code" {
		t.Errorf("expected 'explain this code', got %q", msg)
	}
}

func TestParseCommand_AtPrefix(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("@claude explain this code")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude], got %v", names)
	}
	if msg != "explain this code" {
		t.Errorf("expected 'explain this code', got %q", msg)
	}
}

func TestParseCommand_MultiAgent(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("@cc @cx hello")
	if len(names) != 2 || names[0] != "claude" || names[1] != "codex" {
		t.Errorf("expected [claude codex], got %v", names)
	}
	if msg != "hello" {
		t.Errorf("expected 'hello', got %q", msg)
	}
}

func TestParseCommand_MultiAgentDedup(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("@cc @cc hello")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude] (deduped), got %v", names)
	}
	if msg != "hello" {
		t.Errorf("expected 'hello', got %q", msg)
	}
}

func TestParseCommand_SwitchOnly(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("/claude")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude], got %v", names)
	}
	if msg != "" {
		t.Errorf("expected empty message, got %q", msg)
	}
}

func TestParseCommand_Alias(t *testing.T) {
	h := newTestHandler()
	names, msg := h.parseCommand("/cc write a function")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude] from /cc alias, got %v", names)
	}
	if msg != "write a function" {
		t.Errorf("expected 'write a function', got %q", msg)
	}
}

func TestParseCommand_CustomAlias(t *testing.T) {
	h := newTestHandler()
	h.customAliases = map[string]string{"ai": "claude", "c": "claude"}
	names, msg := h.parseCommand("/ai hello")
	if len(names) != 1 || names[0] != "claude" {
		t.Errorf("expected [claude] from custom alias, got %v", names)
	}
	if msg != "hello" {
		t.Errorf("expected 'hello', got %q", msg)
	}
}

func TestResolveAlias(t *testing.T) {
	h := newTestHandler()
	tests := map[string]string{
		"cc":  "claude",
		"cx":  "codex",
		"oc":  "openclaw",
		"cs":  "cursor",
		"km":  "kimi",
		"gm":  "gemini",
		"ocd": "opencode",
	}
	for alias, want := range tests {
		got := h.resolveAlias(alias)
		if got != want {
			t.Errorf("resolveAlias(%q) = %q, want %q", alias, got, want)
		}
	}
	if got := h.resolveAlias("unknown"); got != "unknown" {
		t.Errorf("resolveAlias(unknown) = %q, want %q", got, "unknown")
	}
	h.customAliases = map[string]string{"cc": "custom-claude"}
	if got := h.resolveAlias("cc"); got != "custom-claude" {
		t.Errorf("resolveAlias(cc) with custom = %q, want custom-claude", got)
	}
}

func TestBuildHelpText(t *testing.T) {
	text := buildHelpText()
	if text == "" {
		t.Error("help text is empty")
	}
	if !strings.Contains(text, "/info") {
		t.Error("help text should mention /info")
	}
	if !strings.Contains(text, "/help") {
		t.Error("help text should mention /help")
	}
	if !strings.Contains(text, "/status") {
		t.Error("help text should mention /status")
	}
	if !strings.Contains(text, "/cancel") {
		t.Error("help text should mention /cancel")
	}
}

func TestTaskControlStatusAndCancel(t *testing.T) {
	h := newTestHandler()
	if got := h.buildTaskStatus("user-1"); !strings.Contains(got, "任务状态：空闲") {
		t.Fatalf("unexpected idle status: %q", got)
	}
	if got := h.cancelActiveTask("user-1"); got != "当前没有正在执行的任务。" {
		t.Fatalf("unexpected idle cancellation: %q", got)
	}

	task := newActiveTask(context.Background())
	h.activeTasks.Store("user-1", task)
	if got := h.buildTaskStatus("user-1"); !strings.Contains(got, "任务状态：运行中") {
		t.Fatalf("unexpected active status: %q", got)
	}
	if got := h.cancelActiveTask("user-1"); !strings.Contains(got, "已请求取消当前任务") {
		t.Fatalf("unexpected cancellation result: %q", got)
	}
	if got := h.cancelActiveTask("user-1"); got != "当前任务正在取消，请稍候。" {
		t.Fatalf("unexpected duplicate cancellation result: %q", got)
	}
}

func TestExtractImagesReturnsEveryImageItem(t *testing.T) {
	first := &ilink.ImageItem{URL: "https://example.com/one.png"}
	second := &ilink.ImageItem{URL: "https://example.com/two.png"}
	got := extractImages(ilink.WeixinMessage{ItemList: []ilink.MessageItem{
		{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "分析图片"}},
		{Type: ilink.ItemTypeImage, ImageItem: first},
		{Type: ilink.ItemTypeImage, ImageItem: second},
	}})
	if len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("extractImages() = %#v", got)
	}
}

func TestExtractFilesReturnsEveryFileItem(t *testing.T) {
	first := &ilink.FileItem{FileName: "report.pdf"}
	second := &ilink.FileItem{FileName: "source.zip"}
	got := extractFiles(ilink.WeixinMessage{ItemList: []ilink.MessageItem{
		{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "检查"}},
		{Type: ilink.ItemTypeFile, FileItem: first},
		{Type: ilink.ItemTypeFile, FileItem: second},
	}})
	if len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("extractFiles() = %#v", got)
	}
}
