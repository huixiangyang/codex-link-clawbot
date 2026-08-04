package visual

import (
	"context"
	"html/template"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNormalizeCardCalculatesAndClampsHeight(t *testing.T) {
	short := normalizeCard(Card{Title: "短卡片"})
	if short.Height != minCanvasHeight {
		t.Fatalf("short card height = %d, want %d", short.Height, minCanvasHeight)
	}
	long := normalizeCard(Card{Options: make([]Option, 60)})
	if long.Height != maxCanvasHeight {
		t.Fatalf("long card height = %d, want %d", long.Height, maxCanvasHeight)
	}
}

func TestThemeForTimeUsesLocalDaylightWindow(t *testing.T) {
	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	tests := []struct {
		name string
		hour int
		min  int
		want Theme
	}{
		{name: "before daylight", hour: 6, min: 59, want: ThemeNight},
		{name: "daylight starts", hour: 7, want: ThemeDay},
		{name: "daylight remains", hour: 18, min: 59, want: ThemeDay},
		{name: "night starts", hour: 19, want: ThemeNight},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 8, 5, test.hour, test.min, 0, 0, zone)
			if got := ThemeForTime(now); got != test.want {
				t.Fatalf("ThemeForTime(%s) = %q, want %q", now.Format(time.RFC3339), got, test.want)
			}
		})
	}
}

func TestPrepareCardSelectsThemeAndDenseColumns(t *testing.T) {
	now := time.Date(2026, 8, 5, 10, 24, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	card := prepareCard(Card{
		Title:    "掌上控制台",
		Subtitle: "微信里的本地 Codex",
		Facts: []Fact{
			{Label: "版本", Value: "v1.7.0"},
			{Label: "会话", Value: "设计开发"},
			{Label: "状态", Value: "运行中"},
			{Label: "任务", Value: "3"},
			{Label: "目录", Value: "weclaw"},
		},
		Options: []Option{
			{Number: "1", Label: "会话"},
			{Number: "2", Label: "任务状态"},
			{Number: "3", Label: "任务记录 · 4 条"},
			{Number: "4", Label: "运行中心"},
		},
	}, now)
	if card.Theme != ThemeDay {
		t.Fatalf("day card theme = %q", card.Theme)
	}
	if card.FactColumns != 3 || card.OptionColumns != 2 {
		t.Fatalf("dense columns = facts:%d options:%d", card.FactColumns, card.OptionColumns)
	}
	if card.Options[2].Label != "任务记录 · 4 条" || card.Options[2].DisplayLabel != "任务记录" || card.Options[2].Meta != "4 条" {
		t.Fatalf("option metadata = %#v", card.Options[2])
	}
	if card.Height < minCanvasHeight || card.Height > 1300 {
		t.Fatalf("dense card height = %d", card.Height)
	}

	long := normalizeCard(Card{
		Facts:   []Fact{{Value: strings.Repeat("很长的数据内容", 4)}, {Value: "短内容"}, {Value: "短内容"}},
		Options: []Option{{Label: "查看这个会话的完整运行状态"}, {Label: "归档当前正在使用的会话"}, {Label: "切换到另一个已有会话"}, {Label: "返回上一级控制中心"}},
	})
	if long.FactColumns != 2 || long.OptionColumns != 1 {
		t.Fatalf("long content columns = facts:%d options:%d", long.FactColumns, long.OptionColumns)
	}
}

func TestPrepareDocumentSelectsNightTheme(t *testing.T) {
	now := time.Date(2026, 8, 5, 22, 8, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	document := prepareDocument(Document{Height: 1100}, now)
	if document.Theme != ThemeNight {
		t.Fatalf("night document theme = %q", document.Theme)
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
	if !strings.Contains(got, `class="night neutral`) {
		t.Fatalf("card template did not render the normalized night theme")
	}
	for _, redundant := range []string{"LOCAL CODEX", "DAYLIGHT", "NIGHT", "W /", `class="arrow"`} {
		if strings.Contains(got, redundant) {
			t.Fatalf("card template still contains redundant element %q", redundant)
		}
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
	if !strings.Contains(got, `class="night"`) {
		t.Fatalf("document template did not render the normalized night theme")
	}
	for _, redundant := range []string{"MOBILE READING", "CODEX RESPONSE", "DAYLIGHT", "NIGHT", "page-watermark"} {
		if strings.Contains(got, redundant) {
			t.Fatalf("document template still contains redundant element %q", redundant)
		}
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
	renderer, err := NewRenderer(Config{
		BrowserCommand: browser,
		RootDir:        t.TempDir(),
		MaxConcurrent:  1,
		Now: func() time.Time {
			return time.Date(2026, 8, 5, 10, 24, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := renderer.Render(context.Background(), Card{
		Variant:  VariantHome,
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
	nightArtifact, err := renderer.Render(context.Background(), Card{Theme: ThemeNight, Title: "夜间控制卡"})
	if err != nil {
		t.Fatal(err)
	}
	defer nightArtifact.Cleanup()
	dayLuma := renderedCornerLuma(t, artifact.Path)
	nightLuma := renderedCornerLuma(t, nightArtifact.Path)
	if dayLuma < 180 || nightLuma > 70 || dayLuma-nightLuma < 120 {
		t.Fatalf("rendered theme luma = day:%d night:%d", dayLuma, nightLuma)
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

func renderedCornerLuma(t *testing.T, path string) uint32 {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoded, _, err := image.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := decoded.At(20, 20).RGBA()
	return (2126*r + 7152*g + 722*b) / 10000 / 257
}
