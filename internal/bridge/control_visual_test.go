package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/thread"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
)

type fakeControlVisualRenderer struct {
	path                 string
	err                  error
	documentErr          error
	card                 visual.Card
	directory            visual.Directory
	workbench            visual.Workbench
	threadMap            visual.ThreadMap
	review               visual.Review
	documents            []visual.Document
	cleanedUp            bool
	renderCalls          int
	directoryRenderCalls int
	workbenchRenderCalls int
	threadMapRenderCalls int
	reviewRenderCalls    int
	documentRenderCalls  int
}

func (r *fakeControlVisualRenderer) RenderReview(_ context.Context, review visual.Review) (*visual.Artifact, error) {
	r.reviewRenderCalls++
	r.review = review
	if r.err != nil {
		return nil, r.err
	}
	return &visual.Artifact{Path: r.path, Width: 1080, Height: 1200, Cleanup: func() { r.cleanedUp = true }}, nil
}

func (r *fakeControlVisualRenderer) RenderThreadMap(_ context.Context, threadMap visual.ThreadMap) (*visual.Artifact, error) {
	r.threadMapRenderCalls++
	r.threadMap = threadMap
	if r.err != nil {
		return nil, r.err
	}
	return &visual.Artifact{Path: r.path, Width: 1080, Height: 1180, Cleanup: func() { r.cleanedUp = true }}, nil
}

func (r *fakeControlVisualRenderer) RenderWorkbench(_ context.Context, workbench visual.Workbench) (*visual.Artifact, error) {
	r.workbenchRenderCalls++
	r.workbench = workbench
	if r.err != nil {
		return nil, r.err
	}
	return &visual.Artifact{Path: r.path, Width: 1080, Height: 780, Cleanup: func() { r.cleanedUp = true }}, nil
}

func (r *fakeControlVisualRenderer) RenderDirectory(_ context.Context, directory visual.Directory) (*visual.Artifact, error) {
	r.directoryRenderCalls++
	r.directory = directory
	if r.err != nil {
		return nil, r.err
	}
	return &visual.Artifact{Path: r.path, Width: 1080, Height: 1600, Cleanup: func() { r.cleanedUp = true }}, nil
}

func (r *fakeControlVisualRenderer) RenderDocument(_ context.Context, document visual.Document) (*visual.Artifact, error) {
	r.documentRenderCalls++
	r.documents = append(r.documents, document)
	if r.documentErr != nil {
		return nil, r.documentErr
	}
	return &visual.Artifact{Path: r.path, Width: 1080, Height: document.Height, Cleanup: func() { r.cleanedUp = true }}, nil
}

func (r *fakeControlVisualRenderer) Render(_ context.Context, card visual.Card) (*visual.Artifact, error) {
	r.renderCalls++
	r.card = card
	if r.err != nil {
		return nil, r.err
	}
	return &visual.Artifact{Path: r.path, Width: 1080, Height: 1200, Cleanup: func() { r.cleanedUp = true }}, nil
}

func TestControlCardFromMainMenu(t *testing.T) {
	card := controlCardFromText("codex-link-clawbot\n\n版本：v1.4.0-runtime.1\nCodex 线程：视觉交互开发\ncodex-link-clawbot 执行：空闲\n\n1  Codex 线程\n2  codex-link-clawbot 执行状态\n3  codex-link-clawbot 运行状态\n\n回复数字即可，0 退出。")
	if card.Variant != visual.VariantHome || card.Title != "codex-link-clawbot" {
		t.Fatalf("main card identity = %#v", card)
	}
	if len(card.Facts) != 3 || card.Facts[0].Label != "版本" || card.Facts[1].Label != "Codex 线程" || card.Facts[1].Value != "视觉交互开发" {
		t.Fatalf("main card facts = %#v", card.Facts)
	}
	if len(card.Options) != 3 || card.Options[2].Label != "codex-link-clawbot 运行状态" {
		t.Fatalf("main card options = %#v", card.Options)
	}
	if card.Footer != "回复数字即可，0 退出。" {
		t.Fatalf("main card footer = %q", card.Footer)
	}
}

func TestCommandDirectoryBuildsStructuredFourSectionView(t *testing.T) {
	handler, _ := newSessionHandler(t)
	page := handler.buildCommandDirectory(context.Background(), "owner-1")
	if page.visual == nil || page.visual.Directory == nil {
		t.Fatalf("directory page has no structured view: %#v", page)
	}
	directory := *page.visual.Directory
	if directory.Title != "Codex 全部功能" || directory.Subtitle != "按领域浏览 Codex 与 codex-link-clawbot 控制能力" || len(directory.Facts) != 3 || len(directory.Sections) != 4 {
		t.Fatalf("directory = %#v", directory)
	}
	if directory.Facts[0].Label != "工作空间" || !strings.Contains(directory.Facts[0].Value, "1 个") {
		t.Fatalf("directory facts = %#v", directory.Facts)
	}
	if first := directory.Sections[0]; first.Code != "1" || first.Icon != "activity" || first.Items[0].Code != "11" || first.Items[0].Label != "全局总览" {
		t.Fatalf("first directory section = %#v", first)
	}
	if development := directory.Sections[1]; development.Icon != "folder-kanban" || development.Items[2].Label != "模型与权限" || development.Items[2].Meta != "/model · /permissions · 仅作功能对照" {
		t.Fatalf("development directory section = %#v", development)
	}
}

func TestGlobalWorkbenchBuildsStructuredTargetThreadsAndActions(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建线程 首页重构")
	page := handler.buildGlobalWorkbench(context.Background(), "owner-1")
	if page.visual == nil || page.visual.Workbench == nil {
		t.Fatalf("workbench page has no structured view: %#v", page)
	}
	workbench := *page.visual.Workbench
	controlCount := 0
	for _, group := range workbench.Controls {
		controlCount += len(group.Controls)
	}
	if workbench.State != "就绪" || len(workbench.Facts) != 4 || len(workbench.Threads) != 1 || len(workbench.Actions) != 5 || len(workbench.Controls) != 3 || controlCount != 15 {
		t.Fatalf("workbench = %#v", workbench)
	}
	if workbench.Target.Title != "首页重构" || !workbench.Target.Available || !workbench.Threads[0].Current {
		t.Fatalf("workbench content = %#v", workbench)
	}
	if workbench.Threads[0].Project != "Workspace" || workbench.Threads[0].Directory == "" || workbench.Threads[0].Directory != workbench.Target.Directory {
		t.Fatalf("workbench session context = %#v target=%#v", workbench.Threads[0], workbench.Target)
	}
	if workbench.Actions[0].Code != "5" || workbench.Actions[0].Meta != "" || workbench.Actions[4].Icon != "refresh-cw" || workbench.Controls[0].Controls[0].Code != "11" {
		t.Fatalf("workbench actions = %#v", workbench.Actions)
	}
	if got := workbench.Controls[0].Controls[1].Reference; got != "/resume" {
		t.Fatalf("workbench command reference = %q", got)
	}
	if got := workbench.Controls[1].Controls[2].Reference; got != "/clear … /mcp" {
		t.Fatalf("workbench command catalog reference = %q", got)
	}
}

func TestWorkbenchDirectoryFieldPreservesPathEdges(t *testing.T) {
	short := "/root/CODES/codex-link-clawbot"
	if got := workbenchDirectoryField(short); got != short {
		t.Fatalf("short directory = %q", got)
	}
	spaced := "/root/project with  two spaces"
	if got := workbenchDirectoryField(spaced); got != spaced {
		t.Fatalf("spaced directory = %q", got)
	}
	long := "/root/CODES/very-long-parent-directory/another-long-directory/codex-link-clawbot/internal/bridge"
	got := workbenchDirectoryField(long)
	if len([]rune(got)) != 54 || !strings.HasPrefix(got, "/root/CODES/") || !strings.HasSuffix(got, "-link-clawbot/internal/bridge") || !strings.Contains(got, "…") {
		t.Fatalf("compacted directory = %q", got)
	}
}

func TestThreadRelationsBuildStructuredNativeGraph(t *testing.T) {
	handler, runtime := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建线程 关系根")
	rootID := fmt.Sprintf("019fcc03-fc8b-7842-a812-%012d", runtime.next)
	_ = controlReply(t, handler, "owner-1", "分叉当前线程")
	if _, err := handler.sessions.UseGlobalThread(context.Background(), "owner-1", thread.Workspace{ID: "workspace", Name: "Workspace", Root: runtime.cwd}, rootID, runtime); err != nil {
		t.Fatal(err)
	}
	page := handler.buildCurrentThreadRelations(context.Background(), "owner-1")
	if page.visual == nil || page.visual.ThreadMap == nil {
		t.Fatalf("thread map page has no structured view: %#v", page)
	}
	threadMap := *page.visual.ThreadMap
	if threadMap.Workspace != "Workspace" || threadMap.Current.Title != "关系根" || threadMap.Parent != nil || len(threadMap.Children) != 1 {
		t.Fatalf("thread map = %#v", threadMap)
	}
	if threadMap.Children[0].Code != "1" || threadMap.Children[0].Role != visual.ThreadMapChild || len(threadMap.Actions) != 2 {
		t.Fatalf("thread map nodes = %#v", threadMap)
	}
}

func TestRenderWorkbenchPreviewWithNumberedControls(t *testing.T) {
	previewRoot := strings.TrimSpace(os.Getenv("CODEX_LINK_CLAWBOT_DIRECTORY_PREVIEW_DIR"))
	if previewRoot == "" {
		t.Skip("preview output is not requested")
	}
	browser, err := visual.ResolveBrowser("")
	if err != nil {
		t.Skipf("Chromium is not installed: %v", err)
	}
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建线程 首页全局工作台重构")
	page := handler.buildGlobalWorkbench(context.Background(), "owner-1")
	workbench := *page.visual.Workbench
	workbench.Style = presentation.StyleAtelier
	workbench.Theme = visual.ThemeNight
	renderer, err := visual.NewRenderer(visual.Config{
		BrowserCommand: browser, RootDir: previewRoot, MaxConcurrent: 1,
		Now: func() time.Time { return time.Date(2026, 8, 11, 21, 0, 0, 0, time.Local) },
	})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := renderer.RenderWorkbench(context.Background(), workbench)
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Cleanup()
	data, err := os.ReadFile(artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(previewRoot, "workbench-numbered-controls-night.png"), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestControlCardFromRuntimeCenter(t *testing.T) {
	reply := "codex-link-clawbot 运行状态\ncodex-link-clawbot：运行中\n版本：v1.4.0-runtime.1\n已运行：2 小时 5 分\nCodex：运行中\n协议：Codex 应用服务\n模型：使用 Codex 默认配置\nCodex 工作目录：/workspace\nCodex PID：4242\n\n1  有效配置状态\n2  刷新 codex-link-clawbot 状态\n\n回复数字操作，0 返回。"
	card := controlCardFromText(reply)
	if card.Variant != visual.VariantSystem || card.Title != "codex-link-clawbot 运行状态" {
		t.Fatalf("runtime card identity = %#v", card)
	}
	if len(card.Facts) != 8 || card.Facts[0].Label != "codex-link-clawbot" || card.Facts[7].Label != "Codex PID" {
		t.Fatalf("runtime card facts = %#v", card.Facts)
	}
	if len(card.Options) != 2 || card.Options[1].Label != "刷新 codex-link-clawbot 状态" {
		t.Fatalf("runtime card options = %#v", card.Options)
	}
}

func TestControlCardFromCodexCatalogKeepsNumericOperations(t *testing.T) {
	reply := "Codex 操作 · 模型与能力\n\n页码：1 / 1\n\n" +
		"1  选择模型与推理\n" +
		"2  浏览技能\n\n" +
		"回复数字执行，0 返回可用命令。"
	card := controlCardFromText(reply)
	if card.Variant != visual.VariantSession || card.Title != "Codex 操作 · 模型与能力" {
		t.Fatalf("command card identity = %#v", card)
	}
	if len(card.Options) != 2 || card.Options[0].Label != "选择模型与推理" || card.Options[1].Label != "浏览技能" {
		t.Fatalf("command card options = %#v", card.Options)
	}
}

func TestControlCardUsesWarningSemanticsForArchiveConfirmation(t *testing.T) {
	reply := "准备归档线程：视觉交互开发\n\n1  确认归档\n\n回复 1 确认，0 返回。"
	card := controlCardFromText(reply)
	if card.Variant != visual.VariantWarning || card.Title != "归档确认" || card.Subtitle != "视觉交互开发" {
		t.Fatalf("archive card = %#v", card)
	}
	if got := controlCaption(reply, card); got != "回复 1 确认，0 返回。" {
		t.Fatalf("archive caption = %q", got)
	}
}

func TestControlCardFromBrowsableSessionDetail(t *testing.T) {
	reply := "线程详情\n名称：登录排障\n短编号：00000001\n状态：空闲\n位置：可用\n目录：/workspace\n摘要：检查微信登录失败原因\n\n1  切换到这个线程\n2  归档这个线程\n3  返回线程列表\n\n回复数字管理，0 返回原列表。"
	card := controlCardFromText(reply)
	if card.Variant != visual.VariantSession || card.Title != "线程详情" {
		t.Fatalf("session detail card identity = %#v", card)
	}
	if len(card.Facts) != 6 || card.Facts[3].Label != "位置" || card.Facts[5].Label != "摘要" {
		t.Fatalf("session detail card facts = %#v", card.Facts)
	}
	if len(card.Options) != 3 || card.Options[0].Label != "切换到这个线程" {
		t.Fatalf("session detail card options = %#v", card.Options)
	}
}

func TestControlCardFromTaskHistory(t *testing.T) {
	reply := "codex-link-clawbot 请求队列\n\n页码：1 / 2\n等待：2\n执行：1\n已暂停：否\n\n1  检查发布流程 · 已完成\n2  分析失败原因 · 失败\n7  下一页 · 2/2\n\n回复数字查看详情，或说“下一页”“上一页”；0 返回。"
	card := controlCardFromText(reply)
	if card.Variant != visual.VariantProgress || card.Title != "codex-link-clawbot 请求队列" {
		t.Fatalf("activity card identity = %#v", card)
	}
	if len(card.Facts) != 4 || card.Facts[3].Label != "已暂停" {
		t.Fatalf("activity card facts = %#v", card.Facts)
	}
	if len(card.Options) != 3 || card.Options[2].Label != "下一页 · 2/2" {
		t.Fatalf("activity card options = %#v", card.Options)
	}
}

func TestHandleMessageSendsSingleVisualWorkbench(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "card.png")
	if err := os.WriteFile(imagePath, []byte("test-png"), 0o600); err != nil {
		t.Fatal(err)
	}
	renderer := &fakeControlVisualRenderer{path: imagePath}
	var sent []ilink.SendMessageRequest
	var uploaded []byte
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/getuploadurl":
			_, _ = fmt.Fprintf(w, `{"ret":0,"upload_full_url":%q}`, server.URL+"/upload")
		case "/upload":
			uploaded, _ = io.ReadAll(r.Body)
			w.Header().Set("X-Encrypted-Param", "download-token")
			_, _ = w.Write([]byte("ok"))
		case "/ilink/bot/sendmessage":
			var request ilink.SendMessageRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode message: %v", err)
			}
			sent = append(sent, request)
			_, _ = w.Write([]byte(`{"ret":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	handler, runtime := newSessionHandler(t)
	handler.visual = renderer
	styleStore, err := preference.NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := styleStore.SetStyle("owner-1", presentation.StyleNoir); err != nil {
		t.Fatal(err)
	}
	handler.preferences = styleStore
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL})
	handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
		MessageID: 9201, FromUserID: "owner-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish, ContextToken: "context-visual",
		ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "菜单"}}},
	})

	if runtime.chatThreadID != "" {
		t.Fatalf("visual control unexpectedly started Codex thread %s", runtime.chatThreadID)
	}
	if renderer.workbenchRenderCalls != 1 || renderer.directoryRenderCalls != 0 || renderer.renderCalls != 0 || renderer.workbench.Style != presentation.StyleNoir || len(renderer.workbench.Actions) != 5 || !renderer.cleanedUp {
		t.Fatalf("renderer state = workbench calls:%d directory calls:%d card calls:%d workbench:%#v cleanup:%v", renderer.workbenchRenderCalls, renderer.directoryRenderCalls, renderer.renderCalls, renderer.workbench, renderer.cleanedUp)
	}
	if len(uploaded) == 0 {
		t.Fatal("visual card was not uploaded")
	}
	if len(sent) != 1 {
		t.Fatalf("sent messages = %d, want exactly one workbench image", len(sent))
	}
	if item := sent[0].Msg.ItemList[0]; item.Type != ilink.ItemTypeImage || item.ImageItem == nil {
		t.Fatalf("workbench message = %#v", item)
	}
}

func TestVisualRenderFailureFallsBackToFullText(t *testing.T) {
	renderer := &fakeControlVisualRenderer{err: fmt.Errorf("renderer unavailable")}
	var sent ilink.SendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode fallback: %v", err)
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	handler, _ := newSessionHandler(t)
	handler.visual = renderer
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL})
	handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
		MessageID: 9202, FromUserID: "owner-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ItemList:     []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "菜单"}}},
	})
	if len(sent.Msg.ItemList) != 1 || sent.Msg.ItemList[0].TextItem == nil || !strings.Contains(sent.Msg.ItemList[0].TextItem.Text, "5  全部线程") {
		t.Fatalf("fallback message = %#v", sent.Msg.ItemList)
	}
}

func TestVisualUploadFailureFallsBackToFullTextAndCleansArtifact(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "card.png")
	if err := os.WriteFile(imagePath, []byte("test-png"), 0o600); err != nil {
		t.Fatal(err)
	}
	renderer := &fakeControlVisualRenderer{path: imagePath}
	var sent ilink.SendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/getuploadurl":
			_, _ = w.Write([]byte(`{"ret":7,"errmsg":"upload unavailable"}`))
		case "/ilink/bot/sendmessage":
			if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
				t.Errorf("decode upload fallback: %v", err)
			}
			_, _ = w.Write([]byte(`{"ret":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	handler, _ := newSessionHandler(t)
	handler.visual = renderer
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL})
	handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
		MessageID: 9203, FromUserID: "owner-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish,
		ItemList:     []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "菜单"}}},
	})
	if !renderer.cleanedUp {
		t.Fatal("visual artifact was not cleaned after upload failure")
	}
	if len(sent.Msg.ItemList) != 1 || sent.Msg.ItemList[0].TextItem == nil || !strings.Contains(sent.Msg.ItemList[0].TextItem.Text, "首页 5 分钟内有效") {
		t.Fatalf("upload fallback message = %#v", sent.Msg.ItemList)
	}
}

func TestLongCodexReplyUsesReadingCardAndKeepsCopyableText(t *testing.T) {
	imagePath := filepath.Join(t.TempDir(), "document.png")
	if err := os.WriteFile(imagePath, []byte("test-png"), 0o600); err != nil {
		t.Fatal(err)
	}
	renderer := &fakeControlVisualRenderer{path: imagePath}
	var sent []ilink.SendMessageRequest
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/getuploadurl":
			_, _ = fmt.Fprintf(w, `{"ret":0,"upload_full_url":%q}`, server.URL+"/upload")
		case "/upload":
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("X-Encrypted-Param", "download-token")
			_, _ = w.Write([]byte("ok"))
		case "/ilink/bot/sendmessage":
			var request ilink.SendMessageRequest
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode long reply message: %v", err)
			}
			sent = append(sent, request)
			_, _ = w.Write([]byte(`{"ret":0}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	handler, runtime := newSessionHandler(t)
	handler.visual = renderer
	styleStore, err := preference.NewStore(filepath.Join(t.TempDir(), "preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := styleStore.SetStyle("owner-1", presentation.StyleEditorial); err != nil {
		t.Fatal(err)
	}
	handler.preferences = styleStore
	handler.visualReplyEnabled = true
	handler.visualReplyMinRunes = 20
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL})
	reply := "# 移动端结果\n\n" + strings.Repeat("这是一段适合手机阅读并且需要保留可复制原文的 Codex 回复。", 16)
	handler.sendReplyWithMedia(context.Background(), client, ilink.WeixinMessage{FromUserID: "owner-1", ContextToken: "context-long"}, reply, "", "client-long")

	if renderer.documentRenderCalls == 0 || len(renderer.documents) == 0 || !renderer.cleanedUp {
		t.Fatalf("document renderer state = calls:%d documents:%d cleanup:%v", renderer.documentRenderCalls, len(renderer.documents), renderer.cleanedUp)
	}
	for _, document := range renderer.documents {
		if document.Style != presentation.StyleEditorial {
			t.Fatalf("reading document style = %q", document.Style)
		}
	}
	if len(sent) != renderer.documentRenderCalls {
		t.Fatalf("sent messages = %d, want %d reading pages without a redundant caption", len(sent), renderer.documentRenderCalls)
	}
	if item := sent[0].Msg.ItemList[0]; item.Type != ilink.ItemTypeImage || item.ImageItem == nil {
		t.Fatalf("reading page message = %#v", item)
	}
	_ = controlReply(t, handler, "owner-1", "菜单")

	handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
		MessageID: 9301, FromUserID: "owner-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish, ContextToken: "context-copy",
		ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "文字版"}}},
	})
	if runtime.chatThreadID != "" {
		t.Fatalf("copyable text request unexpectedly reached Codex thread %s", runtime.chatThreadID)
	}
	if _, status, err := handler.controlStates.Load("owner-1"); err != nil || status != controlStateMissing {
		t.Fatal("copyable text retrieval should clear the previous menu state")
	}
	if len(sent) != renderer.documentRenderCalls+1 {
		t.Fatalf("sent messages after text retrieval = %d", len(sent))
	}
	copyItem := sent[len(sent)-1].Msg.ItemList[0].TextItem
	if copyItem == nil || !strings.Contains(copyItem.Text, "移动端结果") || !strings.Contains(copyItem.Text, "可复制原文") {
		t.Fatalf("copyable reply = %#v", copyItem)
	}
}

func TestExpiredCopyableTextRequestNeverStartsCodexTurn(t *testing.T) {
	var sent ilink.SendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode expired copy notice: %v", err)
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	handler, runtime := newSessionHandler(t)
	handler.visualReplies.Store("owner-1", &cachedVisualReply{
		Text: "已经过期的原文", ExpiresAt: time.Now().Add(-time.Second),
	})
	_ = controlReply(t, handler, "owner-1", "菜单")
	client := ilink.NewClient(&ilink.Credentials{
		BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL,
	})
	handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
		MessageID: 9302, FromUserID: "owner-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish, ContextToken: "context-expired",
		ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "文字版"}}},
	})
	if runtime.chatThreadID != "" {
		t.Fatalf("expired copy request unexpectedly reached Codex thread %s", runtime.chatThreadID)
	}
	if _, status, err := handler.controlStates.Load("owner-1"); err != nil || status != controlStateMissing {
		t.Fatal("expired copy request should clear the previous menu state")
	}
	item := sent.Msg.ItemList[0].TextItem
	if item == nil || !strings.Contains(item.Text, "已过期或不存在") || !strings.Contains(item.Text, "30 分钟") {
		t.Fatalf("expired copy notice = %#v", item)
	}
}

func TestLongReplyRenderFailureFallsBackToFullText(t *testing.T) {
	renderer := &fakeControlVisualRenderer{documentErr: fmt.Errorf("document renderer unavailable")}
	var sent ilink.SendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode long reply fallback: %v", err)
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	handler, _ := newSessionHandler(t)
	handler.visual = renderer
	handler.visualReplyEnabled = true
	handler.visualReplyMinRunes = 20
	client := ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL})
	reply := strings.Repeat("长回复必须在渲染失败时完整退回文字。", 20)
	handler.sendReplyWithMedia(context.Background(), client, ilink.WeixinMessage{FromUserID: "owner-1"}, reply, "", "fallback-long")
	if renderer.documentRenderCalls != 1 {
		t.Fatalf("document render calls = %d", renderer.documentRenderCalls)
	}
	if len(sent.Msg.ItemList) != 1 || sent.Msg.ItemList[0].TextItem == nil || !strings.Contains(sent.Msg.ItemList[0].TextItem.Text, "完整退回文字") {
		t.Fatalf("long reply fallback = %#v", sent.Msg.ItemList)
	}
}
