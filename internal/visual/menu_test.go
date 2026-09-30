package visual

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testNumberMenu() Menu {
	menu := Menu{Title: "主菜单", Context: "codex-link-clawbot · 新对话"}
	for i, label := range []string{"新建对话", "切换会话", "工作空间", "会话状态", "最近结果", "回复设置"} {
		menu.Options = append(menu.Options, MenuOption{Number: i + 1, Label: label})
	}
	return menu
}

func TestNumberMenuBoundsAndEscapesContent(t *testing.T) {
	renderer := &Renderer{tmpl: newVisualTestTemplate(t)}
	menu := testNumberMenu()
	menu.Context = `<img src=x onerror=alert(1)>`
	menu.Notice = `<script>alert("x")</script>`
	html, err := renderer.renderMenuHTML(menu)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(html), "<script>") || strings.Contains(string(html), "<img src=") || !strings.Contains(string(html), "&lt;script&gt;") {
		t.Fatal("menu did not escape dynamic content")
	}
	menu.Options = append(menu.Options, MenuOption{Number: 7, Label: "too many"})
	if _, err := renderer.renderMenuHTML(menu); err == nil {
		t.Fatal("unbounded menu accepted")
	}
	menu = testNumberMenu()
	menu.Options[1].Number = 1
	if _, err := renderer.renderMenuHTML(menu); err == nil {
		t.Fatal("duplicate number accepted")
	}
}

func TestMenuRendersWithInstalledChromium(t *testing.T) {
	browser, err := ResolveBrowser("")
	if err != nil {
		t.Skipf("Chromium unavailable: %v", err)
	}
	renderer, err := NewRenderer(Config{BrowserCommand: browser, RootDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	preview := strings.TrimSpace(os.Getenv("CLAWBOT_VISUAL_PREVIEW_DIR"))
	if preview != "" {
		if err := os.MkdirAll(preview, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	home := testNumberMenu()
	list := Menu{Title: "切换会话", Context: "当前：codex-link-clawbot", Page: "2 / 3", Previous: true, Next: true}
	for i, title := range []string{"修复登录后的会话恢复问题", "调整微信数字菜单", "核对发布配置和环境变量", "跨区域商品内容管理与超长项目名称测试"} {
		list.Options = append(list.Options, MenuOption{Number: i + 1, Label: title, Detail: "23fa9081 · codex-link-clawbot", Current: i == 1})
	}
	confirm := Menu{Title: "确认重新执行", Context: "a36f91c4 · 调整微信菜单", Notice: "会新建请求再次执行，可能重复之前已完成的修改。", Options: []MenuOption{{Number: 1, Label: "确认重新执行"}}}
	busy := Menu{Title: "提交失败", Context: "a36f91c4 · 调整微信菜单", Notice: "收到指令时本会话正在执行。本条指令未提交，也不会稍后执行。请选择操作。", Options: []MenuOption{{Number: 1, Label: "刷新会话状态"}, {Number: 2, Label: "打断本次执行"}, {Number: 3, Label: "切换会话"}, {Number: 4, Label: "新建对话"}}}
	for name, menu := range map[string]Menu{"home": home, "threads": list, "confirm": confirm, "busy": busy} {
		artifact, err := renderer.RenderMenu(context.Background(), menu)
		if err != nil {
			t.Fatal(err)
		}
		if artifact.Width != menuCanvasWidth || artifact.Height != prepareMenu(menu).Height*2 {
			t.Fatalf("bad dimensions: %+v", artifact)
		}
		saveVisualPreview(t, preview, "menu-"+name+".png", artifact.Path)
		if preview != "" {
			html, err := renderer.renderMenuHTML(menu)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(preview, "menu-"+name+".html"), html, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		artifact.Cleanup()
		if _, err := os.Stat(artifact.Path); !os.IsNotExist(err) {
			t.Fatal("menu artifact was not cleaned")
		}
	}
}
