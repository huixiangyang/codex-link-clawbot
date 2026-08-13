package visual

import (
	"context"
	"fmt"
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
			{Label: "目录", Value: "codex-link-clawbot"},
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
	tmpl, err := template.New("card.html").Funcs(template.FuncMap{
		"lucide": lucideIcon, "background": backgroundDataURL,
	}).ParseFS(assets, "assets/card.html")
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

func TestEveryStyleProvidesEscapedReviewTemplate(t *testing.T) {
	tmpl := newVisualTestTemplate(t)
	for _, definition := range Styles() {
		t.Run(string(definition.ID), func(t *testing.T) {
			review, err := prepareReview(Review{
				Style: definition.ID, Theme: ThemeDay, Verdict: ReviewVerdictAttention,
				Headline: `<script>alert("x")</script>`, Summary: "审查摘要", Workspace: "codex-link-clawbot",
				Thread: "移动审查", Target: "未提交改动", Highest: "P1",
				Findings: []ReviewFinding{{Priority: "P1", Title: `<img src=x onerror=alert(1)>`, Location: "handler.go:21"}},
				Options:  []Option{{Number: "1", Label: "继续修复 · 当前线程"}}, Footer: "回复 1 继续",
			}, time.Date(2026, 8, 8, 12, 0, 0, 0, time.Local))
			if err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			if err := tmpl.ExecuteTemplate(&output, reviewTemplateName(review.Style), review); err != nil {
				t.Fatal(err)
			}
			html := output.String()
			if strings.Contains(html, `<script>alert`) || strings.Contains(html, `<img src=x`) ||
				!strings.Contains(html, `&lt;script&gt;`) || !strings.Contains(html, `&lt;img`) {
				t.Fatalf("%s review template did not escape dynamic text", definition.ID)
			}
			if !strings.Contains(html, `class="day `+string(definition.ID)+` attention"`) || !strings.Contains(html, "scan-search") && !strings.Contains(html, `<svg`) {
				t.Fatalf("%s review template identity is missing", definition.ID)
			}
		})
	}
}

func TestEveryStyleProvidesEmbeddedBackground(t *testing.T) {
	for _, definition := range Styles() {
		dataURL := string(backgroundDataURL(definition.ID))
		if !strings.HasPrefix(dataURL, "data:image/webp;base64,") || len(dataURL) < 100 {
			t.Fatalf("%s background was not embedded as WebP data", definition.ID)
		}
	}
	if got := backgroundDataURL(Style("../../secret")); got != backgroundDataURL(DefaultStyle) {
		t.Fatal("unknown style did not normalize to the fixed default background")
	}
}

func TestEveryStyleProvidesEscapedDirectoryTemplate(t *testing.T) {
	tmpl := newVisualTestTemplate(t)
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
			if !strings.Contains(output, `class="day `+string(definition.ID)+`"`) {
				t.Fatalf("%s directory template did not expose its style identity", definition.ID)
			}
			if !strings.Contains(output, `<svg viewBox="0 0 24 24" aria-hidden="true">`) {
				t.Fatalf("%s directory template did not render the fixed Lucide icon", definition.ID)
			}
		})
	}
}

func TestDirectoryAcceptsCompleteManagedHomeSurface(t *testing.T) {
	directory := testDirectory()
	actionCount := len(directory.Sections)
	for _, section := range directory.Sections {
		actionCount += len(section.Items)
	}
	if actionCount != 19 {
		t.Fatalf("directory action count = %d", actionCount)
	}
	if _, err := prepareDirectory(directory, time.Date(2026, 8, 6, 12, 0, 0, 0, time.Local)); err != nil {
		t.Fatalf("complete command directory was rejected: %v", err)
	}

	directory.Sections[0].Items = append(directory.Sections[0].Items,
		DirectoryItem{Code: "14", Label: "越界入口一"},
		DirectoryItem{Code: "15", Label: "越界入口二"},
		DirectoryItem{Code: "16", Label: "越界入口三"},
	)
	if _, err := prepareDirectory(directory, time.Date(2026, 8, 6, 12, 0, 0, 0, time.Local)); err == nil || !strings.Contains(err.Error(), "invalid directory section") {
		t.Fatalf("oversized directory section error = %v", err)
	}
}

func TestWorkbenchKeepsRecentThreadsAndQuickActionsBounded(t *testing.T) {
	workbench := testWorkbench()
	prepared, err := prepareWorkbench(workbench, time.Date(2026, 8, 6, 12, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Height != workbenchHeight(len(prepared.Threads)) || prepared.Threads[0].Tone != "live" || prepared.Theme != ThemeDay {
		t.Fatalf("prepared workbench = %#v", prepared)
	}
	workbench.Threads = append(workbench.Threads, WorkbenchThread{Code: "4", Title: "越界线程", Workspace: "Workspace", Status: "空闲"})
	workbench.Threads = append(workbench.Threads, WorkbenchThread{Code: "10", Title: "第五个线程", Workspace: "Workspace", Status: "空闲"})
	if _, err := prepareWorkbench(workbench, time.Now()); err == nil {
		t.Fatal("oversized workbench thread list was accepted")
	}
	invalidFact := testWorkbench()
	invalidFact.Facts[0].Value = " "
	if _, err := prepareWorkbench(invalidFact, time.Now()); err == nil {
		t.Fatal("empty workbench telemetry fact was accepted")
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

func testDirectory() Directory {
	return Directory{
		Title: "Codex 全部功能", Subtitle: "按领域浏览 Codex 与 codex-link-clawbot 控制能力",
		Facts: []Fact{
			{Label: "工作空间", Value: "2 个 · 当前 codex-link-clawbot"},
			{Label: "目标线程", Value: "移动端控制重构"},
			{Label: "codex-link-clawbot 执行", Value: "运行中"},
		},
		Sections: []DirectorySection{
			{Code: "1", Title: "Codex · 全局", Icon: "activity", Items: []DirectoryItem{
				{Code: "11", Label: "全局总览"}, {Code: "12", Label: "全局线程", Meta: "/resume"},
				{Code: "13", Label: "账号与额度", Meta: "/usage"},
			}},
			{Code: "2", Title: "Codex · 工作空间", Icon: "folder-kanban", Items: []DirectoryItem{
				{Code: "21", Label: "工作空间"}, {Code: "22", Label: "目标线程", Meta: "/status"},
				{Code: "23", Label: "模型与权限", Meta: "/model /permissions"},
				{Code: "24", Label: "技能与工具", Meta: "/skills /mcp"}, {Code: "25", Label: "微信可用命令"},
			}},
			{Code: "3", Title: "Codex · 执行", Icon: "list-todo", Items: []DirectoryItem{
				{Code: "31", Label: "新建工作", Meta: "/new"}, {Code: "32", Label: "审查改动", Meta: "/review"},
				{Code: "33", Label: "请求队列"}, {Code: "34", Label: "取消执行"},
			}},
			{Code: "4", Title: "codex-link-clawbot · 远程", Icon: "settings-2", Items: []DirectoryItem{
				{Code: "41", Label: "最近结果与交付箱"}, {Code: "42", Label: "系统健康与诊断"},
				{Code: "43", Label: "呈现与安全"},
			}},
		},
		Footer: "回复编号或直接发送 /command · 0 返回全局工作台 · 目录 30 分钟内有效",
	}
}

func testWorkbench() Workbench {
	return Workbench{
		Title: "全局工作台", Subtitle: "从微信统筹 Codex 桌面端、CLI 与远程执行", State: "就绪",
		Facts:  []Fact{{Label: "工作空间", Value: "2 个"}, {Label: "全部线程", Value: "12 个"}, {Label: "运行中", Value: "1 个"}, {Label: "微信队列", Value: "空闲"}},
		Target: WorkbenchTarget{Title: "首页全局工作台重构", Workspace: "codex-link-clawbot", Status: "空闲", Time: "8 分钟前", Available: true},
		Threads: []WorkbenchThread{
			{Code: "1", Title: "登录排障", Workspace: "API", Status: "运行中", Time: "刚刚", Wechat: "微信执行中"},
			{Code: "2", Title: "首页全局工作台重构", Workspace: "codex-link-clawbot", Status: "空闲", Time: "8 分钟前", Current: true},
			{Code: "3", Title: "OSS 数据补偿", Workspace: "SYJ", Status: "未加载", Time: "1 小时前"},
		},
		Actions: []WorkbenchAction{
			{Code: "5", Label: "全部线程", Meta: "/resume", Icon: "messages-square"},
			{Code: "6", Label: "新建线程", Meta: "/new", Icon: "plus"},
			{Code: "7", Label: "执行与队列", Icon: "list-filter"},
			{Code: "8", Label: "工作空间", Icon: "folder-kanban"},
			{Code: "9", Label: "刷新工作台", Icon: "refresh-cw"},
		},
		Commands: testWorkbenchCommands(),
		Footer:   "回复编号操作 · 普通内容进入当前目标 · 0 退出 · 首页 5 分钟内有效",
	}
}

func testWorkbenchCommands() []WorkbenchCommandGroup {
	titles := []string{"会话管理", "切换与状态", "模型与能力"}
	counts := []int{6, 6, 5}
	groups := make([]WorkbenchCommandGroup, 0, len(titles))
	commandNumber := 0
	for groupIndex, title := range titles {
		group := WorkbenchCommandGroup{Title: title}
		for itemIndex := 0; itemIndex < counts[groupIndex]; itemIndex++ {
			commandNumber++
			tone := "native"
			if itemIndex%3 == 2 {
				tone = "adapted"
			}
			group.Commands = append(group.Commands, WorkbenchCommand{
				Label: fmt.Sprintf("功能 %02d", commandNumber), Command: fmt.Sprintf("/command-%02d", commandNumber), Tone: tone,
			})
		}
		groups = append(groups, group)
	}
	return groups
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
	previewRoot := strings.TrimSpace(os.Getenv("CODEX_LINK_CLAWBOT_DIRECTORY_PREVIEW_DIR"))
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
	saveVisualPreview(t, previewRoot, "card-atelier-day.png", artifact.Path)
	nightArtifact, err := renderer.Render(context.Background(), Card{Theme: ThemeNight, Title: "夜间控制卡"})
	if err != nil {
		t.Fatal(err)
	}
	defer nightArtifact.Cleanup()
	saveVisualPreview(t, previewRoot, "card-atelier-night.png", nightArtifact.Path)
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
	saveVisualPreview(t, previewRoot, "document-atelier-day.png", documentArtifact.Path)
	if documentArtifact.Width != CanvasWidth || documentArtifact.Height != documents[0].Height {
		t.Fatalf("document dimensions = %dx%d", documentArtifact.Width, documentArtifact.Height)
	}

	directory := testDirectory()
	directory.Style = StyleAtelier
	directoryArtifact, err := renderer.RenderDirectory(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	defer directoryArtifact.Cleanup()
	saveVisualPreview(t, previewRoot, "directory-atelier-day.png", directoryArtifact.Path)
	if directoryArtifact.Width != CanvasWidth || directoryArtifact.Height != directoryCanvasHeight {
		t.Fatalf("directory dimensions = %dx%d", directoryArtifact.Width, directoryArtifact.Height)
	}
	if info, err := os.Stat(directoryArtifact.Path); err != nil || info.Size() == 0 || info.Size() >= 12<<20 {
		t.Fatalf("directory artifact is invalid: info=%v err=%v", info, err)
	}

	workbench := testWorkbench()
	workbench.Style = StyleAtelier
	workbenchArtifact, err := renderer.RenderWorkbench(context.Background(), workbench)
	if err != nil {
		t.Fatal(err)
	}
	defer workbenchArtifact.Cleanup()
	saveVisualPreview(t, previewRoot, "workbench-atelier-day.png", workbenchArtifact.Path)
	if workbenchArtifact.Width != workbenchCanvasWidth || workbenchArtifact.Height != workbenchHeight(len(workbench.Threads)) {
		t.Fatalf("workbench dimensions = %dx%d", workbenchArtifact.Width, workbenchArtifact.Height)
	}
	nightWorkbench := testWorkbench()
	nightWorkbench.Style = StyleAtelier
	nightWorkbench.Theme = ThemeNight
	nightWorkbenchArtifact, err := renderer.RenderWorkbench(context.Background(), nightWorkbench)
	if err != nil {
		t.Fatal(err)
	}
	defer nightWorkbenchArtifact.Cleanup()
	saveVisualPreview(t, previewRoot, "workbench-atelier-night.png", nightWorkbenchArtifact.Path)

	reviewArtifact, err := renderer.RenderReview(context.Background(), Review{
		Style: StyleAtelier, Verdict: ReviewVerdictAttention, Headline: "发现 2 项需要判断",
		Summary: "优先处理高等级问题；完整证据可以随时取回。", Workspace: "codex-link-clawbot",
		Thread: "移动端审查包", Target: "未提交改动", Highest: "P1",
		Facts: []Fact{
			{Label: "变更", Value: "12 个文件 · +180 / −42"},
			{Label: "验证", Value: "3 项 · 3 通过 · 测试/检查"},
			{Label: "交付", Value: "1 项可再次发送"},
		},
		Findings: []ReviewFinding{
			{Priority: "P1", Title: "避免目标线程在菜单期间漂移", Location: "messaging/review_control.go:73", Detail: "继续修复必须冻结工作空间与线程。"},
			{Priority: "P2", Title: "保留完整审查原文", Location: "messaging/reply_visual.go:126", Detail: "摘要不能替代可复制的审查证据。"},
		},
		Options: []Option{{Number: "1", Label: "继续修复 · 当前线程"}, {Number: "2", Label: "接受结论 · 结束审查"}, {Number: "3", Label: "重新审查 · /review"}},
		Footer:  "回复数字继续；回复“文字版”获取完整审查原文；0 返回 Codex 开发",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reviewArtifact.Cleanup()
	saveVisualPreview(t, previewRoot, "review-atelier-day.png", reviewArtifact.Path)
	if reviewArtifact.Width != CanvasWidth || reviewArtifact.Height != reviewHeight(2, 3) {
		t.Fatalf("review dimensions = %dx%d", reviewArtifact.Width, reviewArtifact.Height)
	}

	commandArtifact, err := renderer.Render(context.Background(), Card{
		Style:    StyleAtelier,
		Variant:  VariantSession,
		Title:    "Codex 命令 · 会话管理",
		Subtitle: "仅显示 codex-link-clawbot 可操作能力",
		Facts: []Fact{
			{Label: "可用命令", Value: "17 个"},
			{Label: "页码", Value: "1 / 1"},
		},
		Options: []Option{
			{Number: "1", Label: "清屏并新建线程 · /clear"},
			{Number: "2", Label: "重命名当前线程 · /rename"},
			{Number: "3", Label: "归档当前线程 · /archive"},
			{Number: "4", Label: "永久删除当前线程 · /delete"},
			{Number: "5", Label: "压缩上下文 · /compact"},
			{Number: "6", Label: "取回最近回答 · /copy"},
		},
		Footer: "回复数字执行 · 0 返回可用命令",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer commandArtifact.Cleanup()
	saveVisualPreview(t, previewRoot, "card-codex-command-catalog.png", commandArtifact.Path)
	if commandArtifact.Width != CanvasWidth || commandArtifact.Height < minCanvasHeight {
		t.Fatalf("command artifact dimensions = %dx%d", commandArtifact.Width, commandArtifact.Height)
	}

	for _, style := range []Style{StyleEditorial, StyleNoir, StyleCute, StyleMinimal} {
		styledDay, err := renderer.Render(context.Background(), Card{Style: style, Title: "风格预览", Facts: []Fact{{Label: "风格", Value: style.Definition().Name}}})
		if err != nil {
			t.Fatal(err)
		}
		defer styledDay.Cleanup()
		saveVisualPreview(t, previewRoot, "card-"+string(style)+"-day.png", styledDay.Path)
		styledNight, err := renderer.Render(context.Background(), Card{Style: style, Theme: ThemeNight, Title: "风格预览"})
		if err != nil {
			t.Fatal(err)
		}
		defer styledNight.Cleanup()
		saveVisualPreview(t, previewRoot, "card-"+string(style)+"-night.png", styledNight.Path)
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
		saveVisualPreview(t, previewRoot, "document-"+string(style)+"-day.png", styledDocumentArtifact.Path)
		if styledDocumentArtifact.Width != CanvasWidth || styledDocumentArtifact.Height != styledDocument.Height {
			t.Fatalf("%s document dimensions = %dx%d", style, styledDocumentArtifact.Width, styledDocumentArtifact.Height)
		}

		for _, theme := range []Theme{ThemeDay, ThemeNight} {
			styledWorkbench := testWorkbench()
			styledWorkbench.Style = style
			styledWorkbench.Theme = theme
			styledWorkbenchArtifact, renderErr := renderer.RenderWorkbench(context.Background(), styledWorkbench)
			if renderErr != nil {
				t.Fatal(renderErr)
			}
			defer styledWorkbenchArtifact.Cleanup()
			saveVisualPreview(t, previewRoot, "workbench-"+string(style)+"-"+string(theme)+".png", styledWorkbenchArtifact.Path)
			if styledWorkbenchArtifact.Width != workbenchCanvasWidth || styledWorkbenchArtifact.Height != workbenchHeight(len(styledWorkbench.Threads)) {
				t.Fatalf("%s %s workbench dimensions = %dx%d", style, theme, styledWorkbenchArtifact.Width, styledWorkbenchArtifact.Height)
			}
		}
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
