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

	"github.com/huixiangyang/weclaw/internal/codex"
	"github.com/huixiangyang/weclaw/internal/ilink"
	"github.com/huixiangyang/weclaw/internal/session"
)

type handlerThreadClient struct {
	next         int
	threads      map[string]codex.ThreadInfo
	archived     map[string]bool
	chatThreadID string
	cwdChanges   []string
	goals        map[string]codex.ThreadGoal
	steered      []string
}

func newHandlerThreadClient() *handlerThreadClient {
	return &handlerThreadClient{
		threads:  make(map[string]codex.ThreadInfo),
		archived: make(map[string]bool),
		goals:    make(map[string]codex.ThreadGoal),
	}
}

func (a *handlerThreadClient) Info() codex.RuntimeInfo {
	return codex.RuntimeInfo{Command: "codex", Cwd: "/workspace", PID: 4242}
}

func (a *handlerThreadClient) SetCwd(cwd string) { a.cwdChanges = append(a.cwdChanges, cwd) }

func (a *handlerThreadClient) StartThread(context.Context) (codex.ThreadInfo, error) {
	a.next++
	id := fmt.Sprintf("019fcc03-fc8b-7842-a812-%012d", a.next)
	thread := codex.ThreadInfo{
		ID: id, Preview: fmt.Sprintf("测试线程 %d", a.next), Cwd: "/workspace",
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

func (a *handlerThreadClient) ForkThread(_ context.Context, threadID string) (codex.ThreadInfo, error) {
	source, ok := a.threads[threadID]
	if !ok {
		return codex.ThreadInfo{}, fmt.Errorf("thread not found")
	}
	a.next++
	id := fmt.Sprintf("019fcc03-fc8b-7842-a812-%012d", a.next)
	forked := source
	forked.ID = id
	forked.Name = ""
	forked.ForkedFromID = threadID
	a.threads[id] = forked
	return forked, nil
}

func (a *handlerThreadClient) SetThreadPinned(_ context.Context, threadID string, pinned bool) (codex.ThreadInfo, error) {
	thread, ok := a.threads[threadID]
	if !ok {
		return codex.ThreadInfo{}, fmt.Errorf("thread not found")
	}
	thread.IsPinned = pinned
	a.threads[threadID] = thread
	return thread, nil
}

func (a *handlerThreadClient) CompactThread(context.Context, string) error { return nil }

func (a *handlerThreadClient) DeleteThread(_ context.Context, threadID string) error {
	if _, ok := a.threads[threadID]; !ok {
		return fmt.Errorf("thread not found")
	}
	deleted := map[string]bool{threadID: true}
	for changed := true; changed; {
		changed = false
		for id, thread := range a.threads {
			if !deleted[id] && deleted[thread.ForkedFromID] {
				deleted[id] = true
				changed = true
			}
		}
	}
	for id := range deleted {
		delete(a.threads, id)
		delete(a.goals, id)
	}
	return nil
}

func (a *handlerThreadClient) SetThreadGoal(_ context.Context, threadID, objective string, tokenBudget *int64) (codex.ThreadGoal, error) {
	goal := codex.ThreadGoal{ThreadID: threadID, Objective: objective, Status: "active", TokenBudget: tokenBudget}
	a.goals[threadID] = goal
	return goal, nil
}

func (a *handlerThreadClient) GetThreadGoal(_ context.Context, threadID string) (codex.ThreadGoal, bool, error) {
	goal, ok := a.goals[threadID]
	return goal, ok, nil
}

func (a *handlerThreadClient) ClearThreadGoal(_ context.Context, threadID string) error {
	delete(a.goals, threadID)
	return nil
}

func (a *handlerThreadClient) SteerThread(_ context.Context, threadID string, request codex.ChatRequest) error {
	a.steered = append(a.steered, threadID+":"+request.Text)
	return nil
}

func (a *handlerThreadClient) ReviewThread(context.Context, string, codex.ReviewTarget, codex.ProgressHandler) (string, error) {
	return "没有发现阻断问题。", nil
}

func (a *handlerThreadClient) ListModels(context.Context) ([]codex.ModelInfo, error) {
	return []codex.ModelInfo{
		{
			ID: "gpt-fast", DisplayName: "快速模型", DefaultReasoningEffort: "low", IsDefault: true,
			SupportedReasoningEfforts: []codex.ReasoningEffort{{Effort: "low"}, {Effort: "medium"}},
		},
		{
			ID: "gpt-deep", DisplayName: "深度模型", DefaultReasoningEffort: "high",
			SupportedReasoningEfforts: []codex.ReasoningEffort{{Effort: "medium"}, {Effort: "high"}},
		},
	}, nil
}

func (a *handlerThreadClient) InspectProject(context.Context, string) (codex.ProjectCapabilities, error) {
	return codex.ProjectCapabilities{Skills: []codex.SkillInfo{{Name: "测试技能", Enabled: true}}, MCPServers: 1, MCPReady: 1}, nil
}

var _ codex.AdvancedThreadClient = (*handlerThreadClient)(nil)
var _ codex.CapabilityClient = (*handlerThreadClient)(nil)

func newSessionHandler(t *testing.T) (*Handler, *handlerThreadClient) {
	t.Helper()
	threadAgent := newHandlerThreadClient()
	handler := NewHandler(threadAgent)
	attachTestControlState(t, handler)
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
	reply, handled := handler.handleControlInput(context.Background(), ownerID, text, false, nextTestControlSource())
	if !handled {
		t.Fatalf("control input %q was not handled", text)
	}
	return reply.Text
}

func TestConversationalSessionFlowCreateCompleteSwitchRenameArchiveRestore(t *testing.T) {
	handler, _ := newSessionHandler(t)
	if got := controlReply(t, handler, "owner-1", "当前线程"); !strings.Contains(got, "当前 WeClaw 项目入口没有 Codex 线程") {
		t.Fatalf("initial current session = %q", got)
	}
	firstReply := controlReply(t, handler, "owner-1", "新建线程 叫登录排障")
	if !strings.Contains(firstReply, "已创建并切换") || !strings.Contains(firstReply, "00000001") {
		t.Fatalf("first new reply = %q", firstReply)
	}
	secondReply := controlReply(t, handler, "owner-1", "创建线程 名为发布检查")
	if !strings.Contains(secondReply, "00000002") {
		t.Fatalf("second new reply = %q", secondReply)
	}
	list := controlReply(t, handler, "owner-1", "线程列表")
	for _, want := range []string{"线程列表", "当前", "发布检查", "登录排障", "回复数字"} {
		if !strings.Contains(list, want) {
			t.Fatalf("session picker missing %q: %q", want, list)
		}
	}
	switched := controlReply(t, handler, "owner-1", "切换线程 登录")
	if !strings.Contains(switched, "已切换线程") || !strings.Contains(switched, "登录排障") {
		t.Fatalf("use reply = %q", switched)
	}
	renamed := controlReply(t, handler, "owner-1", "重命名当前线程 为微信登录修复")
	if !strings.Contains(renamed, "线程已重命名") || !strings.Contains(renamed, "微信登录修复") {
		t.Fatalf("rename reply = %q", renamed)
	}
	detail := controlReply(t, handler, "owner-1", "当前线程")
	if !strings.Contains(detail, "位置：当前") || strings.Contains(detail, "完整编号") || !strings.Contains(detail, "微信登录修复") {
		t.Fatalf("detail reply = %q", detail)
	}
	confirm := controlReply(t, handler, "owner-1", "归档当前线程")
	if !strings.Contains(confirm, "准备归档") || !strings.Contains(confirm, "回复 1 确认") {
		t.Fatalf("archive confirmation = %q", confirm)
	}
	archived := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(archived, "线程已归档") || !strings.Contains(archived, "当前：发布检查") || !strings.Contains(archived, "查看当前线程") {
		t.Fatalf("archive reply = %q", archived)
	}
	archivedList := controlReply(t, handler, "owner-1", "已归档线程")
	if !strings.Contains(archivedList, "恢复线程") || !strings.Contains(archivedList, "微信登录修复") {
		t.Fatalf("archived list = %q", archivedList)
	}
	restored := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(restored, "线程已恢复") {
		t.Fatalf("restore reply = %q", restored)
	}
}

func TestMutatingIntentReceiptPreventsDuplicateSessionCreation(t *testing.T) {
	handler, runtime := newSessionHandler(t)
	sourceKey := nextTestControlSource()
	first, handled := handler.handleControlInput(context.Background(), "owner-1", "新建线程 幂等检查", false, sourceKey)
	if !handled || !strings.Contains(first.Text, "已创建并切换") || runtime.next != 1 {
		t.Fatalf("first session creation = %#v, handled=%v next=%d", first, handled, runtime.next)
	}
	duplicate, handled := handler.handleControlInput(context.Background(), "owner-1", "新建线程 幂等检查", false, sourceKey)
	if !handled || !strings.Contains(duplicate.Text, "不会重复执行") || runtime.next != 1 {
		t.Fatalf("duplicate session creation = %#v, handled=%v next=%d", duplicate, handled, runtime.next)
	}
}

func TestMutatingIntentRejectsMissingStableSource(t *testing.T) {
	handler, runtime := newSessionHandler(t)
	result, handled := handler.handleControlInput(context.Background(), "owner-1", "新建线程 无来源", false, "")
	if !handled || !strings.Contains(result.Text, "没有稳定来源编号") || runtime.next != 0 {
		t.Fatalf("missing source mutation = %#v, handled=%v next=%d", result, handled, runtime.next)
	}
}

func TestControlMenuAndNumericNavigation(t *testing.T) {
	handler, _ := newSessionHandler(t)
	main := controlReply(t, handler, "owner-1", "/")
	for _, want := range []string{"WeClaw 操作总览", "能力边界：Codex 工作能力与 WeClaw 管理能力分层", "版本：dev", "[1]  Codex · 工作台", "12  新建线程", "[2]  WeClaw · 请求", "[5]  WeClaw · 设置", "[6]  WeClaw · 诊断", "回复编号直接操作"} {
		if !strings.Contains(main, want) {
			t.Fatalf("main menu missing %q: %q", want, main)
		}
	}
	prompt := controlReply(t, handler, "owner-1", "12")
	if !strings.Contains(prompt, "发送线程名称") {
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
		_ = controlReply(t, handler, "owner-1", fmt.Sprintf("新建线程 线程 %02d", index))
	}

	first := controlReply(t, handler, "owner-1", "线程列表")
	for _, want := range []string{"页码：1 / 3", "总数：14", "下一页 · 2/3", "线程 14", "线程 09"} {
		if !strings.Contains(first, want) {
			t.Fatalf("first session page missing %q: %q", want, first)
		}
	}
	second := controlReply(t, handler, "owner-1", "下一页")
	for _, want := range []string{"页码：2 / 3", "上一页 · 1/3", "下一页 · 3/3", "线程 08", "线程 03"} {
		if !strings.Contains(second, want) {
			t.Fatalf("second session page missing %q: %q", want, second)
		}
	}
	third := controlReply(t, handler, "owner-1", "下页")
	for _, want := range []string{"页码：3 / 3", "上一页 · 2/3", "线程 02", "线程 01"} {
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
		_ = controlReply(t, handler, "owner-1", fmt.Sprintf("新建线程 线程 %02d", index))
	}

	_ = controlReply(t, handler, "owner-1", "线程列表")
	second := controlReply(t, handler, "owner-1", "下一页")
	if !strings.Contains(second, "页码：2 / 2") || !strings.Contains(second, "线程 02") {
		t.Fatalf("second browser page = %q", second)
	}
	detail := controlReply(t, handler, "owner-1", "1")
	for _, want := range []string{
		"线程详情", "名称：线程 02", "摘要：测试线程 2", "1  切换到这个线程",
		"2  归档这个线程", "3  返回线程列表",
	} {
		if !strings.Contains(detail, want) {
			t.Fatalf("browser detail missing %q: %q", want, detail)
		}
	}
	if strings.Contains(detail, "已切换线程") {
		t.Fatalf("browsing a session switched it immediately: %q", detail)
	}
	back := controlReply(t, handler, "owner-1", "0")
	if !strings.Contains(back, "页码：2 / 2") {
		t.Fatalf("browser detail lost its source page: %q", back)
	}
}

func TestSessionSearchOpensSafeDetailAndCanSwitch(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建线程 登录排障")
	_ = controlReply(t, handler, "owner-1", "新建线程 发布检查")

	prompt := controlReply(t, handler, "owner-1", "搜索线程")
	if !strings.Contains(prompt, "发送名称、短编号") {
		t.Fatalf("search prompt = %q", prompt)
	}
	results := controlReply(t, handler, "owner-1", "登排")
	for _, want := range []string{"线程列表：登排", "筛选：登排", "登录排障"} {
		if !strings.Contains(results, want) {
			t.Fatalf("search results missing %q: %q", want, results)
		}
	}
	detail := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(detail, "线程详情") || !strings.Contains(detail, "1  切换到这个线程") {
		t.Fatalf("search detail = %q", detail)
	}
	switched := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(switched, "已切换线程") || !strings.Contains(switched, "登录排障") {
		t.Fatalf("search switch result = %q", switched)
	}
}

func TestSessionDetailArchivesNonCurrentSessionWithConfirmation(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建线程 保留当前")
	_ = controlReply(t, handler, "owner-1", "新建线程 待归档")
	_ = controlReply(t, handler, "owner-1", "切换线程 保留当前")
	_ = controlReply(t, handler, "owner-1", "线程列表")
	detail := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(detail, "名称：待归档") || !strings.Contains(detail, "2  归档这个线程") {
		t.Fatalf("non-current detail = %q", detail)
	}
	confirm := controlReply(t, handler, "owner-1", "2")
	if !strings.Contains(confirm, "准备归档线程：待归档") || !strings.Contains(confirm, "回复 1 确认") {
		t.Fatalf("non-current archive confirmation = %q", confirm)
	}
	archived := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(archived, "线程已归档") || !strings.Contains(archived, "当前：保留当前") {
		t.Fatalf("non-current archive result = %q", archived)
	}
}

func TestCurrentSessionDetailOffersQuickManagement(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建线程 视觉管理")
	detail := controlReply(t, handler, "owner-1", "当前线程")
	for _, want := range []string{"当前线程", "视觉管理", "1  重命名线程", "2  分叉线程", "6  清除线程目标", "9  切换其他线程", "10  归档线程"} {
		if !strings.Contains(detail, want) {
			t.Fatalf("session detail missing %q: %q", want, detail)
		}
	}
	renamePrompt := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(renamePrompt, "发送新的线程名称") {
		t.Fatalf("detail rename prompt = %q", renamePrompt)
	}
	renamed := controlReply(t, handler, "owner-1", "移动端管理")
	if !strings.Contains(renamed, "线程已重命名") || !strings.Contains(renamed, "移动端管理") {
		t.Fatalf("detail rename result = %q", renamed)
	}
}

func TestSessionMutationSuccessCardsKeepManagementFlowOpen(t *testing.T) {
	handler, _ := newSessionHandler(t)
	created := controlReply(t, handler, "owner-1", "新建线程 成功闭环")
	for _, want := range []string{
		"已创建并切换到新线程", "1  查看当前线程", "2  线程列表", "3  Codex 线程",
		"或直接发送内容开始对话",
	} {
		if !strings.Contains(created, want) {
			t.Fatalf("create success missing %q: %q", want, created)
		}
	}
	detail := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(detail, "当前线程") || !strings.Contains(detail, "位置：当前") {
		t.Fatalf("success follow-up detail = %q", detail)
	}
}

func TestTaskStatusOffersRefreshAndConfirmedCancellation(t *testing.T) {
	handler, cancel := testHandlerWithRunningTask(t, "owner-1")
	defer cancel()

	status := controlReply(t, handler, "owner-1", "状态")
	for _, want := range []string{"WeClaw 执行状态：运行中", "1  刷新状态", "2  WeClaw 请求队列", "3  取消当前执行"} {
		if !strings.Contains(status, want) {
			t.Fatalf("task status missing %q: %q", want, status)
		}
	}
	confirm := controlReply(t, handler, "owner-1", "3")
	if !strings.Contains(confirm, "准备取消 WeClaw 当前执行") || !strings.Contains(confirm, "1  确认取消执行") {
		t.Fatalf("task cancel confirmation = %q", confirm)
	}
	cancelled := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(cancelled, "已请求取消 WeClaw 当前执行") {
		t.Fatalf("task cancel result = %q", cancelled)
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
	if len(sent.Msg.ItemList) != 1 || sent.Msg.ItemList[0].TextItem == nil || !strings.Contains(sent.Msg.ItemList[0].TextItem.Text, "回复编号直接操作") {
		t.Fatalf("sent menu = %#v", sent.Msg.ItemList)
	}
}

func TestSessionCompletionOffersCandidatesThenAcceptsNumber(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建线程 发布检查")
	_ = controlReply(t, handler, "owner-1", "新建线程 发布排障")
	completion := controlReply(t, handler, "owner-1", "切换线程 发布")
	for _, want := range []string{"选择线程：发布", "发布检查", "发布排障", "回复数字"} {
		if !strings.Contains(completion, want) {
			t.Fatalf("completion missing %q: %q", want, completion)
		}
	}
	switched := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(switched, "已切换线程") {
		t.Fatalf("numeric completion reply = %q", switched)
	}
}

func TestSessionCompletionPrefersAnExactTitle(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建线程 发布")
	_ = controlReply(t, handler, "owner-1", "新建线程 发布检查")
	switched := controlReply(t, handler, "owner-1", "切换线程 发布")
	if !strings.Contains(switched, "已切换线程") || !strings.Contains(switched, "名称：发布\n") {
		t.Fatalf("exact title should win over substring candidates: %q", switched)
	}
}

func TestControlChoiceDoesNotConsumeOrdinaryCodexText(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "/")
	if reply, handled := handler.handleControlInput(context.Background(), "owner-1", "请检查项目测试", false, nextTestControlSource()); handled || reply != (ActionResult{}) {
		t.Fatalf("ordinary text should leave menu and reach Codex: reply=%#v handled=%v", reply, handled)
	}
	if _, status, err := handler.controlStates.Load("owner-1"); err != nil || status != controlStateMissing {
		t.Fatal("ordinary text should clear the pending menu")
	}
}

func TestExpiredControlStateRejectsNumber(t *testing.T) {
	handler, _ := newSessionHandler(t)
	if _, err := handler.controlStates.Put("owner-1", controlState{
		View: viewSessionCenter, Mode: controlChoice, ExpiresAt: time.Now().Add(-time.Second),
		Options: []controlOption{{Action: actionSessionMenu}}, Back: controlOption{Action: actionMain},
	}); err != nil {
		t.Fatal(err)
	}
	if reply, handled := handler.handleControlInput(context.Background(), "owner-1", "1", false, nextTestControlSource()); !handled || !strings.Contains(reply.Text, "操作已经过期") {
		t.Fatalf("expired state result: reply=%#v handled=%v", reply, handled)
	}
	if _, status, err := handler.controlStates.Load("owner-1"); err != nil || status != controlStateMissing {
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

func TestTaskSessionUsesOwnedExplicitThread(t *testing.T) {
	handler, threadAgent := newSessionHandler(t)
	projectID := session.DefaultProjectID
	thread, err := handler.sessions.OpenTaskThread(context.Background(), "owner-1", projectID, "", threadAgent, "检查项目")
	if err != nil {
		t.Fatal(err)
	}
	reply, err := threadAgent.ChatThread(context.Background(), thread.ID, codex.ChatRequest{Text: "检查项目"})
	if err != nil || reply != "显式线程回复" {
		t.Fatalf("task thread reply = %q, %v", reply, err)
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
	if got := threadSearchTitle(markerOnly); got != "未命名线程" {
		t.Fatalf("marker-only threadSearchTitle() = %q", got)
	}
}

func TestSessionCommandsDoNotExposeForeignThreads(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建线程 私有线程")
	got := controlReply(t, handler, "owner-2", "切换线程 00000001")
	if !strings.Contains(got, "没有找到") {
		t.Fatalf("foreign use reply = %q", got)
	}
}

func TestAdvancedCodexThreadControlsUseChineseFlow(t *testing.T) {
	handler, runtime := newSessionHandler(t)
	created := controlReply(t, handler, "owner-1", "新建线程 高级控制")
	if !strings.Contains(created, "已创建并切换到新线程") {
		t.Fatalf("create thread = %q", created)
	}
	forked := controlReply(t, handler, "owner-1", "分叉当前线程")
	if !strings.Contains(forked, "已从当前历史分叉") || !strings.Contains(forked, "高级控制 · 分支") {
		t.Fatalf("fork thread = %q", forked)
	}
	pinned := controlReply(t, handler, "owner-1", "置顶当前线程")
	if !strings.Contains(pinned, "已置顶当前线程") {
		t.Fatalf("pin thread = %q", pinned)
	}
	goal := controlReply(t, handler, "owner-1", "设置线程目标为 完成中文重构")
	if !strings.Contains(goal, "线程目标已设置") || !strings.Contains(goal, "完成中文重构") {
		t.Fatalf("thread goal = %q", goal)
	}
	models := controlReply(t, handler, "owner-1", "线程模型")
	if !strings.Contains(models, "1  快速模型 · 当前") || !strings.Contains(models, "2  深度模型") {
		t.Fatalf("model picker = %q", models)
	}
	efforts := controlReply(t, handler, "owner-1", "2")
	if !strings.Contains(efforts, "推理强度") || !strings.Contains(efforts, "1  中") || !strings.Contains(efforts, "2  高 · 当前") {
		t.Fatalf("effort picker = %q", efforts)
	}
	selected := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(selected, "模型：深度模型") || !strings.Contains(selected, "推理强度：中") {
		t.Fatalf("effort selection = %q", selected)
	}
	review := controlReply(t, handler, "owner-1", "代码审查")
	if !strings.Contains(review, "代码审查") || !strings.Contains(review, "没有发现阻断问题") {
		t.Fatalf("review = %q", review)
	}
	confirm := controlReply(t, handler, "owner-1", "永久删除线程")
	if !strings.Contains(confirm, "准备永久删除线程") || !strings.Contains(confirm, "不可恢复") {
		t.Fatalf("delete confirmation = %q", confirm)
	}
	deleted := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(deleted, "已永久删除") || len(runtime.threads) != 1 {
		t.Fatalf("delete result = %q threads=%#v", deleted, runtime.threads)
	}
}

func TestGoalStatusUsesChineseLabels(t *testing.T) {
	tests := map[string]string{
		"active":        "进行中",
		"paused":        "已暂停",
		"blocked":       "受阻",
		"usageLimited":  "用量受限",
		"budgetLimited": "预算用尽",
		"complete":      "已完成",
	}
	for status, want := range tests {
		if got := displayGoalStatus(status); got != want {
			t.Fatalf("displayGoalStatus(%q) = %q, want %q", status, got, want)
		}
	}
}
