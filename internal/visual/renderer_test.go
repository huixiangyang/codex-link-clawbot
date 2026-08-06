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

func TestNormalizeDocumentUsesDocumentHeightBounds(t *testing.T) {
	short := normalizeDocument(Document{})
	if short.Height != minDocumentHeight {
		t.Fatalf("short document height = %d, want %d", short.Height, minDocumentHeight)
	}
	long := normalizeDocument(Document{Height: maxCanvasHeight})
	if long.Height != maxDocumentHeight {
		t.Fatalf("long document height = %d, want %d", long.Height, maxDocumentHeight)
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
	if !strings.Contains(got, `class="night atelier neutral`) {
		t.Fatalf("card template did not render the normalized night theme")
	}
	for _, redundant := range []string{"LOCAL CODEX", "DAYLIGHT", "NIGHT", "W /", `class="arrow"`} {
		if strings.Contains(got, redundant) {
			t.Fatalf("card template still contains redundant element %q", redundant)
		}
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
	for _, definition := range Styles() {
		t.Run(string(definition.ID), func(t *testing.T) {
			singleHTML, renderErr := renderer.renderDocumentHTML(normalizeDocument(Document{
				Style: definition.ID, Blocks: []DocumentBlock{{Kind: "paragraph", Text: "正文直接开始"}},
				PageNumber: 1, TotalPages: 1, Footer: "回复文字版获取原文",
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
			if !strings.Contains(single, "回复文字版获取原文") {
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
			if !strings.Contains(middle, ">2 / 3<") || strings.Contains(middle, "只应出现在第一页") || strings.Contains(middle, "回复文字版") {
				t.Fatalf("middle-page chrome is invalid")
			}
		})
	}
}

func TestEveryStyleProvidesEscapedCardAndDocumentTemplates(t *testing.T) {
	tmpl := newVisualTestTemplate(t)
	renderer := &Renderer{tmpl: tmpl}
	for _, definition := range Styles() {
		t.Run(string(definition.ID), func(t *testing.T) {
			cardHTML, err := renderer.renderHTML(normalizeCard(Card{
				Style: definition.ID, Theme: ThemeDay, Title: `<b>控制卡</b>`,
				Options: []Option{{Number: "1", Label: `<img src=x onerror=alert(1)>`}},
			}))
			if err != nil {
				t.Fatal(err)
			}
			cardOutput := string(cardHTML)
			if !strings.Contains(cardOutput, `class="day `+string(definition.ID)) || strings.Contains(cardOutput, `<img src=x`) || !strings.Contains(cardOutput, `&lt;img`) {
				t.Fatalf("style card output is invalid")
			}

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

func TestEveryStyleProvidesEscapedDirectoryTemplate(t *testing.T) {
	tmpl := newVisualTestTemplate(t)
	styleMarkers := map[Style]string{
		StyleEditorial: "MOBILE CONTROL EDITION",
		StyleAtelier:   "OPERATIONS GRID",
		StyleNoir:      "DIRECTORY 01 / 06",
		StyleCute:      "MOBILE COMMAND BOOK",
		StyleMinimal:   "CONTROL DIRECTORY / 01",
	}
	for _, definition := range Styles() {
		t.Run(string(definition.ID), func(t *testing.T) {
			directory := testDirectory()
			directory.Style = definition.ID
			directory.Title = `<script>alert("x")</script>`
			directory.Sections[0].Items[0].Label = `<img src=x onerror=alert(1)>`
			prepared, err := prepareDirectory(directory, time.Date(2026, 8, 5, 10, 24, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60)))
			if err != nil {
				t.Fatal(err)
			}
			var htmlOutput strings.Builder
			if err := tmpl.ExecuteTemplate(&htmlOutput, directoryTemplateName(prepared.Style), prepared); err != nil {
				t.Fatal(err)
			}
			output := htmlOutput.String()
			if strings.Contains(output, `<script>alert`) || strings.Contains(output, `<img src=x`) ||
				!strings.Contains(output, `&lt;script&gt;`) || !strings.Contains(output, `&lt;img`) {
				t.Fatalf("%s directory template did not escape dynamic text", definition.ID)
			}
			if !strings.Contains(output, styleMarkers[definition.ID]) {
				t.Fatalf("%s directory template did not expose its style identity", definition.ID)
			}
			if !strings.Contains(output, `<svg viewBox="0 0 24 24" aria-hidden="true">`) {
				t.Fatalf("%s directory template did not render the fixed Lucide icon", definition.ID)
			}
		})
	}
}

func newVisualTestTemplate(t *testing.T) *template.Template {
	t.Helper()
	tmpl, err := template.New("visual").Funcs(template.FuncMap{"lucide": lucideIcon}).ParseFS(assets, "assets/*.html")
	if err != nil {
		t.Fatal(err)
	}
	return tmpl
}

func testDirectory() Directory {
	return Directory{
		Title: "操作总览", Subtitle: "稳定编号，一步直达",
		Facts: []Fact{
			{Label: "版本", Value: "v2.8.0"}, {Label: "项目", Value: "WeClaw"},
			{Label: "会话", Value: "移动端控制重构"}, {Label: "任务", Value: "运行中"},
			{Label: "回答", Value: "自适应"}, {Label: "队列", Value: "2 项等待"},
		},
		Sections: []DirectorySection{
			{Code: "1", Title: "会话管理", Icon: "messages-square", Items: []DirectoryItem{
				{Code: "11", Label: "新建会话"}, {Code: "12", Label: "重命名当前会话"},
				{Code: "13", Label: "切换会话"}, {Code: "14", Label: "搜索会话"},
				{Code: "15", Label: "查看当前会话"}, {Code: "16", Label: "归档当前会话"},
				{Code: "17", Label: "恢复归档会话"},
			}},
			{Code: "2", Title: "项目与工作流", Icon: "folder-kanban", Items: []DirectoryItem{
				{Code: "21", Label: "切换项目"}, {Code: "22", Label: "运行快捷任务", Meta: "3 项"},
				{Code: "23", Label: "新建快捷任务"}, {Code: "24", Label: "管理快捷任务"},
				{Code: "25", Label: "保存最近结果"},
			}},
			{Code: "3", Title: "任务管理", Icon: "list-todo", Items: []DirectoryItem{
				{Code: "31", Label: "查看当前任务"}, {Code: "32", Label: "最近结果"},
				{Code: "33", Label: "暂停队列"}, {Code: "34", Label: "取消当前任务"},
				{Code: "35", Label: "清空等待任务"},
			}},
			{Code: "4", Title: "回答与视觉", Icon: "palette", Items: []DirectoryItem{
				{Code: "41", Label: "自适应回答", Meta: "当前"}, {Code: "42", Label: "阅读卡回答"},
				{Code: "43", Label: "语音回答"}, {Code: "44", Label: "刊物风格"},
				{Code: "45", Label: "构筑风格"}, {Code: "46", Label: "黑标风格"},
				{Code: "47", Label: "可爱风格"}, {Code: "48", Label: "简洁风格"},
			}},
			{Code: "5", Title: "工具与内容", Icon: "package-open", Items: []DirectoryItem{
				{Code: "51", Label: "素材与交付"}, {Code: "52", Label: "链接素材"},
				{Code: "53", Label: "交付记录"}, {Code: "54", Label: "自动化中心", Meta: "2 项"},
				{Code: "55", Label: "语音简报"},
			}},
			{Code: "6", Title: "运行与安全", Icon: "shield-check", Items: []DirectoryItem{
				{Code: "61", Label: "为什么没回复"}, {Code: "62", Label: "远程锁定"},
				{Code: "63", Label: "使用说明"}, {Code: "64", Label: "刷新操作总览"},
			}},
		},
		Footer: "回复编号直接操作 · 0 退出 · 总览 30 分钟内有效",
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
	renderRoot := t.TempDir()
	previewRoot := strings.TrimSpace(os.Getenv("WECLAW_DIRECTORY_PREVIEW_DIR"))
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

	directory := testDirectory()
	directory.Style = StyleAtelier
	directoryArtifact, err := renderer.RenderDirectory(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	if previewRoot == "" {
		defer directoryArtifact.Cleanup()
	} else {
		t.Logf("directory preview: %s", directoryArtifact.Path)
	}
	if directoryArtifact.Width != CanvasWidth || directoryArtifact.Height != directoryCanvasHeight {
		t.Fatalf("directory dimensions = %dx%d", directoryArtifact.Width, directoryArtifact.Height)
	}
	if info, err := os.Stat(directoryArtifact.Path); err != nil || info.Size() == 0 || info.Size() >= 12<<20 {
		t.Fatalf("directory artifact is invalid: info=%v err=%v", info, err)
	}

	for _, style := range []Style{StyleEditorial, StyleNoir, StyleCute, StyleMinimal} {
		styledDay, err := renderer.Render(context.Background(), Card{Style: style, Title: "风格预览", Facts: []Fact{{Label: "风格", Value: style.Definition().Name}}})
		if err != nil {
			t.Fatal(err)
		}
		defer styledDay.Cleanup()
		styledNight, err := renderer.Render(context.Background(), Card{Style: style, Theme: ThemeNight, Title: "风格预览"})
		if err != nil {
			t.Fatal(err)
		}
		defer styledNight.Cleanup()
		if day, night := renderedCornerLuma(t, styledDay.Path), renderedCornerLuma(t, styledNight.Path); day < 180 || night > 70 || day-night < 120 {
			t.Fatalf("%s rendered luma = day:%d night:%d", style, day, night)
		}

		styledDocument := documents[0]
		styledDocument.Style = style
		styledDocumentArtifact, err := renderer.RenderDocument(context.Background(), styledDocument)
		if err != nil {
			t.Fatal(err)
		}
		defer styledDocumentArtifact.Cleanup()
		if styledDocumentArtifact.Width != CanvasWidth || styledDocumentArtifact.Height != styledDocument.Height {
			t.Fatalf("%s document dimensions = %dx%d", style, styledDocumentArtifact.Width, styledDocumentArtifact.Height)
		}
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
