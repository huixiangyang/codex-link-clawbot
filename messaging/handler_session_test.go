package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/huixiangyang/weclaw/codex"
	"github.com/huixiangyang/weclaw/ilink"
	"github.com/huixiangyang/weclaw/session"
)

type handlerThreadClient struct {
	next         int
	threads      map[string]codex.ThreadInfo
	archived     map[string]bool
	chatThreadID string
}

func newHandlerThreadClient() *handlerThreadClient {
	return &handlerThreadClient{
		threads:  make(map[string]codex.ThreadInfo),
		archived: make(map[string]bool),
	}
}

func (a *handlerThreadClient) Info() codex.RuntimeInfo {
	return codex.RuntimeInfo{Command: "codex", Cwd: "/workspace", PID: 4242}
}

func (a *handlerThreadClient) SetCwd(string) {}

func (a *handlerThreadClient) StartThread(context.Context) (codex.ThreadInfo, error) {
	a.next++
	id := fmt.Sprintf("019fcc03-fc8b-7842-a812-%012d", a.next)
	thread := codex.ThreadInfo{
		ID: id, Preview: fmt.Sprintf("测试会话 %d", a.next), Cwd: "/workspace",
		CreatedAt: int64(100 + a.next), UpdatedAt: int64(100 + a.next),
		Status: codex.ThreadStatus{Type: "idle"},
	}
	a.threads[id] = thread
	return thread, nil
}

func (a *handlerThreadClient) ResumeThread(_ context.Context, threadID string) (codex.ThreadInfo, error) {
	thread, ok := a.threads[threadID]
	if !ok || a.archived[threadID] {
		return codex.ThreadInfo{}, fmt.Errorf("thread unavailable")
	}
	return thread, nil
}

func (a *handlerThreadClient) ReadThread(_ context.Context, threadID string) (codex.ThreadInfo, error) {
	thread, ok := a.threads[threadID]
	if !ok {
		return codex.ThreadInfo{}, fmt.Errorf("thread not found")
	}
	return thread, nil
}

func (a *handlerThreadClient) ListThreads(_ context.Context, options codex.ThreadListOptions) (codex.ThreadPage, error) {
	var threads []codex.ThreadInfo
	for id, thread := range a.threads {
		if a.archived[id] == options.Archived {
			threads = append(threads, thread)
		}
	}
	return codex.ThreadPage{Threads: threads}, nil
}

func (a *handlerThreadClient) SetThreadName(_ context.Context, threadID, name string) error {
	thread, ok := a.threads[threadID]
	if !ok {
		return fmt.Errorf("thread not found")
	}
	thread.Name = name
	a.threads[threadID] = thread
	return nil
}

func (a *handlerThreadClient) ArchiveThread(_ context.Context, threadID string) error {
	if _, ok := a.threads[threadID]; !ok {
		return fmt.Errorf("thread not found")
	}
	a.archived[threadID] = true
	return nil
}

func (a *handlerThreadClient) UnarchiveThread(_ context.Context, threadID string) (codex.ThreadInfo, error) {
	thread, ok := a.threads[threadID]
	if !ok || !a.archived[threadID] {
		return codex.ThreadInfo{}, fmt.Errorf("archived thread not found")
	}
	delete(a.archived, threadID)
	return thread, nil
}

func (a *handlerThreadClient) UnsubscribeThread(context.Context, string) error { return nil }

func (a *handlerThreadClient) ChatThread(_ context.Context, threadID string, _ codex.ChatRequest) (string, error) {
	a.chatThreadID = threadID
	return "显式线程回复", nil
}

func newSessionHandler(t *testing.T) (*Handler, *handlerThreadClient) {
	t.Helper()
	threadAgent := newHandlerThreadClient()
	handler := NewHandler(threadAgent)
	attachTestSessionManager(t, handler)
	return handler, threadAgent
}

func attachTestSessionManager(t *testing.T, handler *Handler) {
	t.Helper()
	manager, err := session.NewManager(t.TempDir() + "/session-index.json")
	if err != nil {
		t.Fatal(err)
	}
	handler.SetSessionManager(manager)
}

func controlReply(t *testing.T, handler *Handler, ownerID, text string) string {
	t.Helper()
	reply, handled := handler.handleControlInput(context.Background(), ownerID, text, false)
	if !handled {
		t.Fatalf("control input %q was not handled", text)
	}
	return reply
}

func TestConversationalSessionFlowCreateCompleteSwitchRenameArchiveRestore(t *testing.T) {
	handler, _ := newSessionHandler(t)
	if got := controlReply(t, handler, "owner-1", "当前会话"); !strings.Contains(got, "当前没有会话") {
		t.Fatalf("initial current session = %q", got)
	}
	firstReply := controlReply(t, handler, "owner-1", "新建会话 叫登录排障")
	if !strings.Contains(firstReply, "已创建并切换") || !strings.Contains(firstReply, "00000001") {
		t.Fatalf("first new reply = %q", firstReply)
	}
	secondReply := controlReply(t, handler, "owner-1", "创建会话 名为发布检查")
	if !strings.Contains(secondReply, "00000002") {
		t.Fatalf("second new reply = %q", secondReply)
	}
	list := controlReply(t, handler, "owner-1", "会话列表")
	for _, want := range []string{"会话列表", "当前", "发布检查", "登录排障", "回复数字"} {
		if !strings.Contains(list, want) {
			t.Fatalf("session picker missing %q: %q", want, list)
		}
	}
	switched := controlReply(t, handler, "owner-1", "切换会话 登录")
	if !strings.Contains(switched, "已切换会话") || !strings.Contains(switched, "登录排障") {
		t.Fatalf("use reply = %q", switched)
	}
	renamed := controlReply(t, handler, "owner-1", "重命名当前会话 为微信登录修复")
	if !strings.Contains(renamed, "会话已重命名") || !strings.Contains(renamed, "微信登录修复") {
		t.Fatalf("rename reply = %q", renamed)
	}
	detail := controlReply(t, handler, "owner-1", "当前会话")
	if !strings.Contains(detail, "位置：当前") || strings.Contains(detail, "完整编号") || !strings.Contains(detail, "微信登录修复") {
		t.Fatalf("detail reply = %q", detail)
	}
	confirm := controlReply(t, handler, "owner-1", "归档当前会话")
	if !strings.Contains(confirm, "准备归档") || !strings.Contains(confirm, "回复 1 确认") {
		t.Fatalf("archive confirmation = %q", confirm)
	}
	archived := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(archived, "会话已归档") || !strings.Contains(archived, "当前：发布检查") || !strings.Contains(archived, "查看当前会话") {
		t.Fatalf("archive reply = %q", archived)
	}
	archivedList := controlReply(t, handler, "owner-1", "已归档会话")
	if !strings.Contains(archivedList, "恢复会话") || !strings.Contains(archivedList, "微信登录修复") {
		t.Fatalf("archived list = %q", archivedList)
	}
	restored := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(restored, "会话已恢复") {
		t.Fatalf("restore reply = %q", restored)
	}
}

func TestControlMenuAndNumericNavigation(t *testing.T) {
	handler, _ := newSessionHandler(t)
	main := controlReply(t, handler, "owner-1", "/")
	for _, want := range []string{"WeClaw", "版本：dev", "1  会话", "3  任务记录", "4  运行中心", "6  使用说明", "回复数字"} {
		if !strings.Contains(main, want) {
			t.Fatalf("main menu missing %q: %q", want, main)
		}
	}
	sessions := controlReply(t, handler, "owner-1", "1")
	for _, want := range []string{"会话", "会话列表", "搜索会话", "新建会话", "归档当前会话"} {
		if !strings.Contains(sessions, want) {
			t.Fatalf("session menu missing %q: %q", want, sessions)
		}
	}
	prompt := controlReply(t, handler, "owner-1", "4")
	if !strings.Contains(prompt, "发送会话名称") {
		t.Fatalf("new session prompt = %q", prompt)
	}
	created := controlReply(t, handler, "owner-1", "菜单创建")
	if !strings.Contains(created, "菜单创建") {
		t.Fatalf("created session reply = %q", created)
	}
}

func TestSessionPickerPaginatesAndAcceptsNaturalNavigation(t *testing.T) {
	handler, _ := newSessionHandler(t)
	for index := 1; index <= 14; index++ {
		_ = controlReply(t, handler, "owner-1", fmt.Sprintf("新建会话 会话 %02d", index))
	}

	first := controlReply(t, handler, "owner-1", "会话列表")
	for _, want := range []string{"页码：1 / 3", "总数：14", "下一页 · 2/3", "会话 14", "会话 09"} {
		if !strings.Contains(first, want) {
			t.Fatalf("first session page missing %q: %q", want, first)
		}
	}
	second := controlReply(t, handler, "owner-1", "下一页")
	for _, want := range []string{"页码：2 / 3", "上一页 · 1/3", "下一页 · 3/3", "会话 08", "会话 03"} {
		if !strings.Contains(second, want) {
			t.Fatalf("second session page missing %q: %q", want, second)
		}
	}
	third := controlReply(t, handler, "owner-1", "下页")
	for _, want := range []string{"页码：3 / 3", "上一页 · 2/3", "会话 02", "会话 01"} {
		if !strings.Contains(third, want) {
			t.Fatalf("third session page missing %q: %q", want, third)
		}
	}
	if strings.Contains(third, "  下一页 ·") {
		t.Fatalf("last session page unexpectedly has next navigation: %q", third)
	}
	previous := controlReply(t, handler, "owner-1", "上一页")
	if !strings.Contains(previous, "页码：2 / 3") {
		t.Fatalf("previous page navigation = %q", previous)
	}
}

func TestSessionBrowserShowsDetailBeforeSwitchAndPreservesPage(t *testing.T) {
	handler, _ := newSessionHandler(t)
	for index := 1; index <= 8; index++ {
		_ = controlReply(t, handler, "owner-1", fmt.Sprintf("新建会话 会话 %02d", index))
	}

	_ = controlReply(t, handler, "owner-1", "会话列表")
	second := controlReply(t, handler, "owner-1", "下一页")
	if !strings.Contains(second, "页码：2 / 2") || !strings.Contains(second, "会话 02") {
		t.Fatalf("second browser page = %q", second)
	}
	detail := controlReply(t, handler, "owner-1", "1")
	for _, want := range []string{
		"会话详情", "名称：会话 02", "摘要：测试会话 2", "1  切换到这个会话",
		"2  归档这个会话", "3  返回会话列表",
	} {
		if !strings.Contains(detail, want) {
			t.Fatalf("browser detail missing %q: %q", want, detail)
		}
	}
	if strings.Contains(detail, "已切换会话") {
		t.Fatalf("browsing a session switched it immediately: %q", detail)
	}
	back := controlReply(t, handler, "owner-1", "0")
	if !strings.Contains(back, "页码：2 / 2") {
		t.Fatalf("browser detail lost its source page: %q", back)
	}
}

func TestSessionSearchOpensSafeDetailAndCanSwitch(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建会话 登录排障")
	_ = controlReply(t, handler, "owner-1", "新建会话 发布检查")

	prompt := controlReply(t, handler, "owner-1", "搜索会话")
	if !strings.Contains(prompt, "发送名称、短编号") {
		t.Fatalf("search prompt = %q", prompt)
	}
	results := controlReply(t, handler, "owner-1", "登排")
	for _, want := range []string{"会话列表：登排", "筛选：登排", "登录排障"} {
		if !strings.Contains(results, want) {
			t.Fatalf("search results missing %q: %q", want, results)
		}
	}
	detail := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(detail, "会话详情") || !strings.Contains(detail, "1  切换到这个会话") {
		t.Fatalf("search detail = %q", detail)
	}
	switched := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(switched, "已切换会话") || !strings.Contains(switched, "登录排障") {
		t.Fatalf("search switch result = %q", switched)
	}
}

func TestSessionDetailArchivesNonCurrentSessionWithConfirmation(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建会话 保留当前")
	_ = controlReply(t, handler, "owner-1", "新建会话 待归档")
	_ = controlReply(t, handler, "owner-1", "切换会话 保留当前")
	_ = controlReply(t, handler, "owner-1", "会话列表")
	detail := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(detail, "名称：待归档") || !strings.Contains(detail, "2  归档这个会话") {
		t.Fatalf("non-current detail = %q", detail)
	}
	confirm := controlReply(t, handler, "owner-1", "2")
	if !strings.Contains(confirm, "准备归档会话：待归档") || !strings.Contains(confirm, "回复 1 确认") {
		t.Fatalf("non-current archive confirmation = %q", confirm)
	}
	archived := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(archived, "会话已归档") || !strings.Contains(archived, "当前：保留当前") {
		t.Fatalf("non-current archive result = %q", archived)
	}
}

func TestCurrentSessionDetailOffersQuickManagement(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建会话 视觉管理")
	detail := controlReply(t, handler, "owner-1", "当前会话")
	for _, want := range []string{"当前会话", "视觉管理", "1  重命名当前会话", "2  切换其他会话", "3  归档当前会话"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("session detail missing %q: %q", want, detail)
		}
	}
	renamePrompt := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(renamePrompt, "发送新的会话名称") {
		t.Fatalf("detail rename prompt = %q", renamePrompt)
	}
	renamed := controlReply(t, handler, "owner-1", "移动端管理")
	if !strings.Contains(renamed, "会话已重命名") || !strings.Contains(renamed, "移动端管理") {
		t.Fatalf("detail rename result = %q", renamed)
	}
}

func TestSessionMutationSuccessCardsKeepManagementFlowOpen(t *testing.T) {
	handler, _ := newSessionHandler(t)
	created := controlReply(t, handler, "owner-1", "新建会话 成功闭环")
	for _, want := range []string{
		"已创建并切换到新会话", "1  查看当前会话", "2  会话列表", "3  会话中心",
		"或直接发送内容开始对话",
	} {
		if !strings.Contains(created, want) {
			t.Fatalf("create success missing %q: %q", want, created)
		}
	}
	detail := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(detail, "当前会话") || !strings.Contains(detail, "位置：当前") {
		t.Fatalf("success follow-up detail = %q", detail)
	}
}

func TestTaskStatusOffersRefreshAndConfirmedCancellation(t *testing.T) {
	handler, _ := newSessionHandler(t)
	task := newActiveTask(context.Background())
	handler.activeTasks.Store("owner-1", task)
	defer func() {
		task.finish()
		handler.activeTasks.Delete("owner-1")
	}()

	status := controlReply(t, handler, "owner-1", "状态")
	for _, want := range []string{"任务状态：运行中", "1  刷新状态", "2  取消当前任务"} {
		if !strings.Contains(status, want) {
			t.Fatalf("task status missing %q: %q", want, status)
		}
	}
	confirm := controlReply(t, handler, "owner-1", "2")
	if !strings.Contains(confirm, "准备取消当前任务") || !strings.Contains(confirm, "1  确认取消任务") {
		t.Fatalf("task cancel confirmation = %q", confirm)
	}
	cancelled := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(cancelled, "已请求取消当前任务") || !task.cancelRequested() {
		t.Fatalf("task cancel result = %q requested=%v", cancelled, task.cancelRequested())
	}
}

func TestHandleMessageRoutesSingleSlashToMenuWithoutStartingCodexTurn(t *testing.T) {
	var sent ilink.SendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode sent menu: %v", err)
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	handler, runtime := newSessionHandler(t)
	client := ilink.NewClient(&ilink.Credentials{
		BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL,
	})
	handler.HandleMessage(context.Background(), client, ilink.WeixinMessage{
		MessageID: 9001, FromUserID: "owner-1", MessageType: ilink.MessageTypeUser,
		MessageState: ilink.MessageStateFinish, ContextToken: "context-1",
		ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "/"}}},
	})

	if runtime.chatThreadID != "" {
		t.Fatalf("single slash unexpectedly started Codex thread %s", runtime.chatThreadID)
	}
	if len(sent.Msg.ItemList) != 1 || sent.Msg.ItemList[0].TextItem == nil || !strings.Contains(sent.Msg.ItemList[0].TextItem.Text, "回复数字即可") {
		t.Fatalf("sent menu = %#v", sent.Msg.ItemList)
	}
}

func TestSessionCompletionOffersCandidatesThenAcceptsNumber(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建会话 发布检查")
	_ = controlReply(t, handler, "owner-1", "新建会话 发布排障")
	completion := controlReply(t, handler, "owner-1", "切换会话 发布")
	for _, want := range []string{"选择会话：发布", "发布检查", "发布排障", "回复数字"} {
		if !strings.Contains(completion, want) {
			t.Fatalf("completion missing %q: %q", want, completion)
		}
	}
	switched := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(switched, "已切换会话") {
		t.Fatalf("numeric completion reply = %q", switched)
	}
}

func TestSessionCompletionPrefersAnExactTitle(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建会话 发布")
	_ = controlReply(t, handler, "owner-1", "新建会话 发布检查")
	switched := controlReply(t, handler, "owner-1", "切换会话 发布")
	if !strings.Contains(switched, "已切换会话") || !strings.Contains(switched, "名称：发布\n") {
		t.Fatalf("exact title should win over substring candidates: %q", switched)
	}
}

func TestControlChoiceDoesNotConsumeOrdinaryCodexText(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "/")
	if reply, handled := handler.handleControlInput(context.Background(), "owner-1", "请检查项目测试", false); handled || reply != "" {
		t.Fatalf("ordinary text should leave menu and reach Codex: reply=%q handled=%v", reply, handled)
	}
	if _, exists := handler.controlStates.Load("owner-1"); exists {
		t.Fatal("ordinary text should clear the pending menu")
	}
}

func TestExpiredControlStateDoesNotConsumeNumber(t *testing.T) {
	handler, _ := newSessionHandler(t)
	handler.controlStates.Store("owner-1", &controlState{
		Mode: controlChoice, Prompt: "expired", ExpiresAt: time.Now().Add(-time.Second),
		Options: []controlOption{{Label: "会话", Action: actionSessionMenu}},
	})
	if reply, handled := handler.handleControlInput(context.Background(), "owner-1", "1", false); handled || reply != "" {
		t.Fatalf("expired state should not consume a number: reply=%q handled=%v", reply, handled)
	}
	if _, exists := handler.controlStates.Load("owner-1"); exists {
		t.Fatal("expired control state was not removed")
	}
}

func TestFuzzySessionMatchingSupportsSubsequence(t *testing.T) {
	items := []session.ManagedThread{{Info: codex.ThreadInfo{ID: "thread-1", Name: "微信登录排障"}}}
	matches := matchSessions(items, "微登排")
	if len(matches) != 1 || matches[0].Score != 3 {
		t.Fatalf("subsequence matches = %#v", matches)
	}
}

func TestLegacySlashCommandsAreRejected(t *testing.T) {
	handler, _ := newSessionHandler(t)
	got := controlReply(t, handler, "owner-1", "/sessions")
	if !strings.Contains(got, "斜杠命令已取消") || !strings.Contains(got, "发送一个 /") {
		t.Fatalf("legacy command reply = %q", got)
	}
}

func TestChatWithCodexUsesOwnedExplicitThread(t *testing.T) {
	handler, threadAgent := newSessionHandler(t)
	reply, err := handler.chatWithCodex(context.Background(), "owner-1", codex.ChatRequest{Text: "检查项目"}, nil)
	if err != nil || reply != "显式线程回复" {
		t.Fatalf("chatWithCodex() = %q, %v", reply, err)
	}
	if threadAgent.chatThreadID == "" {
		t.Fatal("ChatThread() did not receive an explicit thread id")
	}
	if threadAgent.threads[threadAgent.chatThreadID].Name != "检查项目" {
		t.Fatalf("automatic session name = %q", threadAgent.threads[threadAgent.chatThreadID].Name)
	}
}

func TestSuggestedSessionNameUsesOnlyCleanUserInput(t *testing.T) {
	tests := []struct {
		name    string
		request codex.ChatRequest
		want    string
	}{
		{name: "first line", request: codex.ChatRequest{Text: "检查发布流程\n[WeClaw 交付物回传]\n/private/path"}, want: "检查发布流程"},
		{name: "image", request: codex.ChatRequest{LocalImages: []string{"/private/image.png"}}, want: "图片分析"},
		{name: "file", request: codex.ChatRequest{LocalFiles: []codex.LocalFile{{Name: "release.patch"}}}, want: "文件分析 · release.patch"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := suggestedSessionName(test.request); got != test.want {
				t.Fatalf("suggestedSessionName() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestThreadPreviewHidesInternalPromptSections(t *testing.T) {
	preview := "用户的真实问题\n\n[WeClaw 交付物回传]\n/root/.weclaw/turns/private/outbox"
	thread := codex.ThreadInfo{Preview: preview}
	if got := threadSearchTitle(thread); got != "用户的真实问题" {
		t.Fatalf("threadSearchTitle() = %q", got)
	}
	markerOnly := codex.ThreadInfo{Preview: "[WeClaw 入站文件]\n/private/file"}
	if got := threadSearchTitle(markerOnly); got != "未命名会话" {
		t.Fatalf("marker-only threadSearchTitle() = %q", got)
	}
}

func TestSessionCommandsDoNotExposeForeignThreads(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建会话 私有会话")
	got := controlReply(t, handler, "owner-2", "切换会话 00000001")
	if !strings.Contains(got, "没有找到") {
		t.Fatalf("foreign use reply = %q", got)
	}
}
