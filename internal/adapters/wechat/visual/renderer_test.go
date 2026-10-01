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

	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
)

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

func TestPrepareDocumentSelectsNightTheme(t *testing.T) {
	now := time.Date(2026, 8, 5, 22, 8, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
	document := prepareDocument(Document{Height: 1100}, now)
	if document.Theme != ThemeNight {
		t.Fatalf("night document theme = %q", document.Theme)
	}
}

func TestNormalizeDocumentUsesDocumentHeightBounds(t *testing.T) {
	short := normalizeDocument(Document{})
	if short.Height != minDocumentHeight {
		t.Fatalf("short document height = %d, want %d", short.Height, minDocumentHeight)
	}
	long := normalizeDocument(Document{Height: maxDocumentHeight + 100})
	if long.Height != maxDocumentHeight {
		t.Fatalf("long document height = %d, want %d", long.Height, maxDocumentHeight)
	}
}

func TestDocumentTemplateEscapesUntrustedText(t *testing.T) {
	tmpl := newVisualTestTemplate(t)
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
	if !strings.Contains(got, `class="night atelier"`) {
		t.Fatalf("document template did not render the normalized night theme")
	}
	for _, redundant := range []string{"MOBILE READING", "CODEX RESPONSE", "DAYLIGHT", "NIGHT", "page-watermark"} {
		if strings.Contains(got, redundant) {
			t.Fatalf("document template still contains redundant element %q", redundant)
		}
	}
}

func TestDocumentTemplateUsesContentFirstChrome(t *testing.T) {
	tmpl := newVisualTestTemplate(t)
	renderer := &Renderer{tmpl: tmpl}
	for _, definition := range presentation.Styles() {
		t.Run(string(definition.ID), func(t *testing.T) {
			singleHTML, renderErr := renderer.renderDocumentHTML(normalizeDocument(Document{
				Style: definition.ID, Blocks: []DocumentBlock{{Kind: "paragraph", Text: "正文直接开始"}},
				PageNumber: 1, TotalPages: 1, Footer: "回复 #，再选 #5 查看结果原文",
			}))
			if renderErr != nil {
				t.Fatal(renderErr)
			}
			single := string(singleHTML)
			for _, unwanted := range []string{">1 / 1<", `<section class="hero">`, `<div class="progress"`} {
				if strings.Contains(single, unwanted) {
					t.Fatalf("single-page document contains %q", unwanted)
				}
			}
			if !strings.Contains(single, "回复 #，再选 #5 查看结果原文") {
				t.Fatal("final page footer is missing")
			}

			middleHTML, renderErr := renderer.renderDocumentHTML(normalizeDocument(Document{
				Style: definition.ID, Title: "只应出现在第一页", Blocks: []DocumentBlock{{Kind: "paragraph", Text: "第二页正文"}},
				PageNumber: 2, TotalPages: 3,
			}))
			if renderErr != nil {
				t.Fatal(renderErr)
			}
			middle := string(middleHTML)
			if !strings.Contains(middle, ">2 / 3<") || strings.Contains(middle, "只应出现在第一页") || strings.Contains(middle, "回复 #，再选 #5") {
				t.Fatalf("middle-page chrome is invalid")
			}
		})
	}
}

func TestEveryStyleProvidesEscapedDocumentTemplates(t *testing.T) {
	tmpl := newVisualTestTemplate(t)
	renderer := &Renderer{tmpl: tmpl}
	for _, definition := range presentation.Styles() {
		t.Run(string(definition.ID), func(t *testing.T) {
			documentHTML, err := renderer.renderDocumentHTML(normalizeDocument(Document{
				Style: definition.ID, Theme: ThemeNight, Title: `<b>阅读卡</b>`, Height: 900,
				Blocks: []DocumentBlock{{Kind: "paragraph", Text: `<script>alert(1)</script>`}},
			}))
			if err != nil {
				t.Fatal(err)
			}
			documentOutput := string(documentHTML)
			if !strings.Contains(documentOutput, `class="night `+string(definition.ID)) || strings.Contains(documentOutput, `<script>alert`) || !strings.Contains(documentOutput, `&lt;script&gt;`) {
				t.Fatalf("style document output is invalid")
			}
		})
	}
}

func TestEveryStyleProvidesEmbeddedBackground(t *testing.T) {
	for _, definition := range presentation.Styles() {
		dataURL := string(backgroundDataURL(definition.ID))
		if !strings.HasPrefix(dataURL, "data:image/webp;base64,") || len(dataURL) < 100 {
			t.Fatalf("%s background was not embedded as WebP data", definition.ID)
		}
	}
	if got := backgroundDataURL(presentation.Style("../../secret")); got != backgroundDataURL(presentation.DefaultStyle) {
		t.Fatal("unknown style did not normalize to the fixed default background")
	}
}

func newVisualTestTemplate(t *testing.T) *template.Template {
	t.Helper()
	tmpl, err := template.New("visual").Funcs(template.FuncMap{
		"lucide": lucideIcon, "background": backgroundDataURL,
	}).ParseFS(assets, "assets/*.html")
	if err != nil {
		t.Fatal(err)
	}
	return tmpl
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
	renderRoot := t.TempDir()
	previewRoot := strings.TrimSpace(os.Getenv("CLAWBOT_VISUAL_PREVIEW_DIR"))
	if previewRoot != "" {
		renderRoot = filepath.Clean(previewRoot)
		if err := os.MkdirAll(renderRoot, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	renderer, err := NewRenderer(Config{
		BrowserCommand: browser,
		RootDir:        renderRoot,
		MaxConcurrent:  1,
		Now: func() time.Time {
			return time.Date(2026, 8, 5, 10, 24, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	documents := PaginateMarkdown("# Codex 回复\n\n完成移动端阅读模式。\n\n- 安全模板\n- 分页显示\n\n```go\nfmt.Println(\"ok\")\n```")
	for _, definition := range presentation.Styles() {
		t.Run(string(definition.ID), func(t *testing.T) {
			luma := map[Theme]uint32{}
			for _, theme := range []Theme{ThemeDay, ThemeNight} {
				document := documents[0]
				document.Style, document.Theme = definition.ID, theme
				artifact, err := renderer.RenderDocument(context.Background(), document)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(artifact.Cleanup)
				if artifact.Width != CanvasWidth || artifact.Height != document.Height {
					t.Fatalf("document dimensions = %dx%d", artifact.Width, artifact.Height)
				}
				saveVisualPreview(t, previewRoot, "document-"+string(definition.ID)+"-"+string(theme)+".png", artifact.Path)
				luma[theme] = renderedCornerLuma(t, artifact.Path)
			}
			if luma[ThemeDay] < 180 || luma[ThemeNight] > 70 || luma[ThemeDay]-luma[ThemeNight] < 120 {
				t.Fatalf("rendered theme luma = day:%d night:%d", luma[ThemeDay], luma[ThemeNight])
			}
		})
	}
}

func saveVisualPreview(t *testing.T, previewRoot, name, source string) {
	t.Helper()
	if previewRoot == "" {
		return
	}
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(previewRoot, name)
	if err := os.WriteFile(destination, data, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("visual preview: %s", destination)
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
