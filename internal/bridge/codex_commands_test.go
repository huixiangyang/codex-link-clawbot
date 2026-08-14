package bridge

import (
	"context"
	"strings"
	"testing"
)

func TestCodexCommandRegistryOnlyProjectsUsableOperations(t *testing.T) {
	if err := validateCodexCommandCatalog(); err != nil {
		t.Fatal(err)
	}
	if got := codexCommandCount(); got != 17 {
		t.Fatalf("微信可用操作 = %d，期望 17", got)
	}
	if len(codexCommandCatalog) != 17 || len(codexCommandByName) != 17 {
		t.Fatalf("操作目录或索引不完整：definitions=%d index=%d", len(codexCommandCatalog), len(codexCommandByName))
	}
	for _, command := range codexCommandCatalog {
		if command.Support != codexCommandNative && command.Support != codexCommandAdapted {
			t.Fatalf("非法操作进入微信目录：%#v", command)
		}
	}
}

func TestCodexOperationsExecuteThroughNumericMenus(t *testing.T) {
	handler, runtime := newSessionHandler(t)

	center := controlReply(t, handler, "owner-1", "菜单")
	if strings.Contains(center, "/command") || !strings.Contains(center, "25  Codex 操作") {
		t.Fatalf("首页没有收口为数字入口：%q", center)
	}
	catalog := controlReply(t, handler, "owner-1", "25")
	if !strings.Contains(catalog, "Codex 操作") || !strings.Contains(catalog, "1  线程与会话") {
		t.Fatalf("Codex 操作目录 = %q", catalog)
	}
	page := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(page, "1  清屏并新建线程") || strings.Contains(page, "/clear") || strings.Contains(page, "/rename") {
		t.Fatalf("线程操作页没有使用纯数字标识：%q", page)
	}
	prompt := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(prompt, "发送线程名称") {
		t.Fatalf("数字操作没有进入新建流程：%q", prompt)
	}
	created := controlReply(t, handler, "owner-1", "命令线程")
	if !strings.Contains(created, "命令线程") || runtime.next != 1 {
		t.Fatalf("数字新建 = %q next=%d", created, runtime.next)
	}

	_ = controlReply(t, handler, "owner-1", "菜单")
	status := controlReply(t, handler, "owner-1", "22")
	if !strings.Contains(status, "当前线程") || !strings.Contains(status, "名称：命令线程") {
		t.Fatalf("编号 22 = %q", status)
	}
}

func TestSlashPrefixedInputIsRejectedBeforeCodex(t *testing.T) {
	handler, runtime := newSessionHandler(t)
	for _, input := range []string{"/", "/status", "/sessions"} {
		result, handled := handler.handleControlInput(context.Background(), "owner-1", input, false, nextTestControlSource())
		if !handled || !strings.Contains(result.Text, "不接受斜杠命令") || runtime.chatThreadID != "" {
			t.Fatalf("斜杠输入 %q = %#v handled=%v thread=%q", input, result, handled, runtime.chatThreadID)
		}
	}
}

func TestCodexCatalogDoesNotExposeClientOnlyOperations(t *testing.T) {
	handler, _ := newSessionHandler(t)
	center := handler.openCodexCommandCenter("owner-1")
	if !strings.Contains(center, "codex-link-clawbot 可用：17 个") || !strings.Contains(center, "线程与会话") || strings.Contains(center, "终端与账号") {
		t.Fatalf("操作中心 = %q", center)
	}
	page := handler.openCodexCommandPage("owner-1", "thread", 1)
	if !strings.Contains(page, "清屏并新建线程") || !strings.Contains(page, "重命名当前线程") || strings.Contains(page, "/diff") || strings.Contains(page, "/clear") {
		t.Fatalf("微信操作页 = %q", page)
	}
	terminal := handler.openCodexCommandPage("owner-1", "terminal", 1)
	if strings.Contains(terminal, "终端与账号") || strings.Contains(terminal, "终端快捷键") {
		t.Fatalf("客户端专属操作泄漏：%q", terminal)
	}
}
