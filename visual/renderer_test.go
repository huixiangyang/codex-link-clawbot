package visual

import (
	"context"
	"html/template"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeCardCalculatesAndClampsHeight(t *testing.T) {
	short := normalizeCard(Card{Title: "短卡片"})
	if short.Height != minCanvasHeight {
		t.Fatalf("short card height = %d, want %d", short.Height, minCanvasHeight)
	}
	long := normalizeCard(Card{Options: make([]Option, 30)})
	if long.Height != maxCanvasHeight {
		t.Fatalf("long card height = %d, want %d", long.Height, maxCanvasHeight)
	}
}

func TestCardTemplateEscapesUntrustedText(t *testing.T) {
	tmpl, err := template.New("card.html").ParseFS(assets, "assets/card.html")
	if err != nil {
		t.Fatal(err)
	}
	renderer := &Renderer{tmpl: tmpl}
	htmlBytes, err := renderer.renderHTML(normalizeCard(Card{
		Title:   `<script>alert("x")</script>`,
		Options: []Option{{Number: "1", Label: `<img src=x onerror=alert(1)>`}},
	}))
	if err != nil {
		t.Fatal(err)
	}
	got := string(htmlBytes)
	if strings.Contains(got, `<script>alert`) || strings.Contains(got, `<img src=x`) {
		t.Fatalf("template emitted raw untrusted markup: %s", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") || !strings.Contains(got, "&lt;img") {
		t.Fatalf("template did not HTML-escape card text")
	}
}

func TestDocumentTemplateEscapesUntrustedText(t *testing.T) {
	tmpl, err := template.New("visual").ParseFS(assets, "assets/*.html")
	if err != nil {
		t.Fatal(err)
	}
	renderer := &Renderer{tmpl: tmpl}
	htmlBytes, err := renderer.renderDocumentHTML(normalizeDocument(Document{
		Title:      `<script>alert("x")</script>`,
		Blocks:     []DocumentBlock{{Kind: "code", Text: `<img src=x onerror=alert(1)>`, Language: `"><script>`}},
		PageNumber: 1,
		TotalPages: 1,
		Height:     1200,
	}))
	if err != nil {
		t.Fatal(err)
	}
	got := string(htmlBytes)
	if strings.Contains(got, `<script>alert`) || strings.Contains(got, `<img src=x`) {
		t.Fatalf("document template emitted raw untrusted markup")
	}
	if !strings.Contains(got, "&lt;script&gt;") || !strings.Contains(got, "&lt;img") {
		t.Fatalf("document template did not escape dynamic text")
	}
}

func TestResolveBrowserValidatesExplicitCommand(t *testing.T) {
	if _, err := ResolveBrowser("chromium"); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Fatalf("relative browser error = %v", err)
	}
	path := filepath.Join(t.TempDir(), "chromium")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveBrowser(path)
	if err != nil || resolved != path {
		t.Fatalf("ResolveBrowser() = %q, %v", resolved, err)
	}
}

func TestSnapBrowserPathsAreRejected(t *testing.T) {
	for _, path := range []string{"/snap", "/snap/bin/chromium", "/snap/chromium/current/usr/lib/chromium"} {
		if !isSnapBrowserPath(path) {
			t.Fatalf("Snap path %q was accepted", path)
		}
	}
	if isSnapBrowserPath("/usr/bin/google-chrome") {
		t.Fatal("regular browser path was classified as Snap")
	}
}

func TestRendererWithInstalledChromium(t *testing.T) {
	browser, err := ResolveBrowser("")
	if err != nil {
		t.Skipf("Chromium is not installed: %v", err)
	}
	renderer, err := NewRenderer(Config{BrowserCommand: browser, RootDir: t.TempDir(), MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := renderer.Render(context.Background(), Card{
		Variant:  VariantHome,
		Kicker:   "WECLAW / CONTROL DECK",
		Title:    "掌上控制台",
		Subtitle: "微信里的本地 Codex",
		Facts:    []Fact{{Label: "会话", Value: "视觉交互开发"}, {Label: "状态", Value: "运行中"}},
		Options:  []Option{{Number: "1", Label: "会话"}, {Number: "2", Label: "任务状态"}},
		Footer:   "回复数字即可，0 退出。",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Cleanup()
	if artifact.Width != CanvasWidth || artifact.Height < minCanvasHeight {
		t.Fatalf("artifact dimensions = %dx%d", artifact.Width, artifact.Height)
	}
	if info, err := os.Stat(artifact.Path); err != nil || info.Size() == 0 {
		t.Fatalf("rendered artifact is unavailable: info=%v err=%v", info, err)
	}

	documents := PaginateMarkdown("# Codex 回复\n\n完成移动端阅读模式。\n\n- 安全模板\n- 分页显示\n\n```go\nfmt.Println(\"ok\")\n```")
	documentArtifact, err := renderer.RenderDocument(context.Background(), documents[0])
	if err != nil {
		t.Fatal(err)
	}
	defer documentArtifact.Cleanup()
	if documentArtifact.Width != CanvasWidth || documentArtifact.Height != documents[0].Height {
		t.Fatalf("document dimensions = %dx%d", documentArtifact.Width, documentArtifact.Height)
	}
}
