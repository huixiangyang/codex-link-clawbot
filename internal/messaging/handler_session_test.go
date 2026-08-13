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

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/config"
	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/project"
	"github.com/huixiangyang/codex-link-clawbot/internal/session"
)

type handlerThreadClient struct {
	next         int
	threads      map[string]codex.ThreadInfo
	archived     map[string]bool
	chatThreadID string
	cwdChanges   []string
	goals        map[string]codex.ThreadGoal
	steered      []string
	reviewText   string
	reviewCalls  []string
	verification codex.ThreadVerificationFacts
	cwd          string
}

func newHandlerThreadClient() *handlerThreadClient {
	return &handlerThreadClient{
		threads:  make(map[string]codex.ThreadInfo),
		archived: make(map[string]bool),
		goals:    make(map[string]codex.ThreadGoal), cwd: "/workspace",
	}
}

func (a *handlerThreadClient) Info() codex.RuntimeInfo {
	return codex.RuntimeInfo{Command: "codex", Cwd: "/workspace", PID: 4242}
}

func (a *handlerThreadClient) SetCwd(cwd string) {
	a.cwd = cwd
	a.cwdChanges = append(a.cwdChanges, cwd)
}

func (a *handlerThreadClient) StartThread(context.Context) (codex.ThreadInfo, error) {
	a.next++
	id := fmt.Sprintf("019fcc03-fc8b-7842-a812-%012d", a.next)
	thread := codex.ThreadInfo{
		ID: id, Preview: fmt.Sprintf("测试线程 %d", a.next), Cwd: a.cwd,
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
		search := strings.ToLower(strings.TrimSpace(options.SearchTerm))
		searchable := strings.ToLower(thread.ID + " " + thread.Name + " " + thread.Preview)
		if a.archived[id] == options.Archived && (search == "" || strings.Contains(searchable, search)) {
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

func (a *handlerThreadClient) UpdateThreadGoalStatus(_ context.Context, threadID, status string) (codex.ThreadGoal, error) {
	goal, ok := a.goals[threadID]
	if !ok {
		return codex.ThreadGoal{}, fmt.Errorf("goal not found")
	}
	goal.Status = status
	a.goals[threadID] = goal
	return goal, nil
}

func (a *handlerThreadClient) SteerThread(_ context.Context, threadID string, request codex.ChatRequest) error {
	a.steered = append(a.steered, threadID+":"+request.Text)
	return nil
}

func (a *handlerThreadClient) ReviewThread(_ context.Context, threadID string, _ codex.ReviewTarget, _ codex.ProgressHandler) (string, error) {
	a.reviewCalls = append(a.reviewCalls, threadID)
	if a.reviewText != "" {
		return a.reviewText, nil
	}
	return "没有发现阻断问题。", nil
}

func (a *handlerThreadClient) ReadThreadVerificationFacts(context.Context, string) (codex.ThreadVerificationFacts, error) {
	return a.verification, nil
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

func (a *handlerThreadClient) ListLoadedThreadIDs(context.Context) ([]string, error) {
	ids := make([]string, 0, len(a.threads))
	for id, thread := range a.threads {
		if thread.Status.Type != "notLoaded" {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (a *handlerThreadClient) ReadAccount(context.Context) (codex.AccountInfo, error) {
	return codex.AccountInfo{Type: "chatgpt", Email: "test@example.com", PlanType: "pro", RequiresOpenAIAuth: true}, nil
}

var _ codex.AdvancedThreadClient = (*handlerThreadClient)(nil)
var _ codex.CapabilityClient = (*handlerThreadClient)(nil)
var _ codex.GoalStatusClient = (*handlerThreadClient)(nil)
var _ codex.GlobalControlClient = (*handlerThreadClient)(nil)

func newSessionHandler(t *testing.T) (*Handler, *handlerThreadClient) {
	t.Helper()
	threadAgent := newHandlerThreadClient()
	handler := NewHandler(threadAgent)
	attachTestControlState(t, handler)
	attachTestSessionManager(t, handler)
	root := t.TempDir()
	projects, err := project.NewManager([]config.ProjectConfig{{ID: "workspace", Name: "Workspace", Root: root}}, t.TempDir()+"/project-state.json")
	if err != nil {
		t.Fatal(err)
	}
	handler.SetProjectManager(projects)
	threadAgent.cwd = root
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
	if got := controlReply(t, handler, "owner-1", "当前线程"); !strings.Contains(got, "当前没有目标线程") {
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
	for _, want := range []string{"Codex 全部线程", "当前目标", "发布检查", "登录排障", "回复线程编号"} {
		if !strings.Contains(list, want) {
			t.Fatalf("session picker missing %q: %q", want, list)
		}
	}
	results := controlReply(t, handler, "owner-1", "切换线程 登录")
	if !strings.Contains(results, "Codex 全局搜索") || !strings.Contains(results, "登录排障") {
		t.Fatalf("global search reply = %q", results)
	}
	switched := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(switched, "已从 Codex 全局目录接管目标线程") || !strings.Contains(switched, "登录排障") {
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
	for _, want := range []string{"Codex 全局工作台", "从微信统筹 Codex 桌面端", "当前目标：尚未选择", "最近线程", "5  全部线程 · /resume", "6  新建线程 · /new", "7  执行与队列", "8  工作空间", "9  刷新工作台", "Codex 功能", "清屏并新建线程 · /clear", "查看 MCP 工具 · /mcp"} {
		if !strings.Contains(main, want) {
			t.Fatalf("main menu missing %q: %q", want, main)
		}
	}
	if strings.Contains(main, "/pets") || strings.Contains(main, "/keymap") || strings.Contains(main, "/experimental") {
		t.Fatalf("client-only commands leaked into main menu: %q", main)
	}
	prompt := controlReply(t, handler, "owner-1", "6")
	if !strings.Contains(prompt, "发送线程名称") {
		t.Fatalf("new session prompt = %q", prompt)
	}
	created := controlReply(t, handler, "owner-1", "菜单创建")
	if !strings.Contains(created, "菜单创建") {
		t.Fatalf("created session reply = %q", created)
	}
}

func TestGlobalWorkbenchShowsRecentStateAndSwitchesTargetDirectly(t *testing.T) {
	handler, runtime := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建线程 当前目标")
	currentID := fmt.Sprintf("019fcc03-fc8b-7842-a812-%012d", runtime.next)
	_ = controlReply(t, handler, "owner-1", "新建线程 最近执行")
	recentID := fmt.Sprintf("019fcc03-fc8b-7842-a812-%012d", runtime.next)
	recent := runtime.threads[recentID]
	recent.Status = codex.ThreadStatus{Type: "active"}
	recent.UpdatedAt = 500
	runtime.threads[recentID] = recent
	if _, err := handler.sessions.UseGlobalThread(context.Background(), "owner-1", session.Workspace{ID: "workspace", Name: "Workspace", Root: runtime.cwd}, currentID, runtime); err != nil {
		t.Fatal(err)
	}

	main := controlReply(t, handler, "owner-1", "/")
	for _, want := range []string{
		"全部线程：2 个", "运行中：1 个", "当前目标：当前目标 ｜ Workspace ｜ 空闲",
		"1  最近执行 ｜ Workspace ｜ 执行中", "2  当前目标 ｜ Workspace ｜ 空闲", "当前目标",
	} {
		if !strings.Contains(main, want) {
			t.Fatalf("workbench missing %q: %q", want, main)
		}
	}
	state, status, err := handler.controlStates.Load("owner-1")
	if err != nil || status != controlStateActive || len(state.Options) != 22 || state.Options[0].Code != "1" || state.Options[0].Value != recentID || state.Options[2].Code != "5" || state.Options[21].Code != "43" {
		t.Fatalf("workbench state = %#v status=%v err=%v", state, status, err)
	}

	selected := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(selected, "已从 Codex 全局目录接管目标线程") || !strings.Contains(selected, "最近执行") {
		t.Fatalf("workbench direct switch = %q", selected)
	}
}

func TestGlobalWorkbenchLimitsRecentThreadSlotsToFour(t *testing.T) {
	handler, _ := newSessionHandler(t)
	for index := 1; index <= 6; index++ {
		_ = controlReply(t, handler, "owner-1", fmt.Sprintf("新建线程 最近 %d", index))
	}
	main := controlReply(t, handler, "owner-1", "/")
	if !strings.Contains(main, "全部线程：6 个") || !strings.Contains(main, "最近 6") || strings.Contains(main, "最近 2 ｜") {
		t.Fatalf("bounded recent threads = %q", main)
	}
	state, status, err := handler.controlStates.Load("owner-1")
	if err != nil || status != controlStateActive || len(state.Options) != 24 || state.Options[3].Code != "4" || state.Options[4].Code != "5" || state.Options[23].Code != "43" {
		t.Fatalf("bounded workbench state = %#v status=%v err=%v", state, status, err)
	}
}

func TestGlobalWorkbenchPinsOlderCurrentThreadIntoCompactList(t *testing.T) {
	handler, runtime := newSessionHandler(t)
	ids := make([]string, 0, 6)
	for index := 1; index <= 6; index++ {
		_ = controlReply(t, handler, "owner-1", fmt.Sprintf("新建线程 会话 %d", index))
		ids = append(ids, fmt.Sprintf("019fcc03-fc8b-7842-a812-%012d", runtime.next))
	}
	if _, err := handler.sessions.UseGlobalThread(context.Background(), "owner-1", session.Workspace{ID: "workspace", Name: "Workspace", Root: runtime.cwd}, ids[0], runtime); err != nil {
		t.Fatal(err)
	}

	main := controlReply(t, handler, "owner-1", "/")
	if !strings.Contains(main, "4  会话 1 ｜ Workspace ｜ 空闲") || !strings.Contains(main, "当前目标") || strings.Contains(main, "4  会话 3 ｜") {
		t.Fatalf("older current thread was not pinned into compact list: %q", main)
	}
	state, status, err := handler.controlStates.Load("owner-1")
	if err != nil || status != controlStateActive || len(state.Options) != 24 || state.Options[3].Value != ids[0] {
		t.Fatalf("pinned workbench state = %#v status=%v err=%v", state, status, err)
	}
}

func TestCodexGlobalOverviewAndAccountAreIndependentOfTargetThread(t *testing.T) {
	handler, runtime := newSessionHandler(t)
	runtime.threads["019fcc03-fc8b-7842-a812-000000000099"] = codex.ThreadInfo{
		ID: "019fcc03-fc8b-7842-a812-000000000099", Name: "桌面端正在执行", Cwd: runtime.cwd,
		UpdatedAt: 999, Status: codex.ThreadStatus{Type: "active"},
	}
	overview := handler.openCodexGlobalOverview(context.Background(), "owner-1")
	for _, want := range []string{"Codex 全局总览", "工作空间：1 个", "活动线程：1 个", "运行中：1 个", "目标线程：未选择"} {
		if !strings.Contains(overview, want) {
			t.Fatalf("global overview missing %q: %q", want, overview)
		}
	}
	threads := handler.openCodexGlobalThreadPage(context.Background(), "owner-1", false, false, "", 1)
	for _, want := range []string{"Codex 全部线程", "仅看运行中线程", "搜索线程 · /resume", "已归档线程"} {
		if !strings.Contains(threads, want) {
			t.Fatalf("global thread center missing %q: %q", want, threads)
		}
	}
	account := handler.openCodexAccount(context.Background(), "owner-1")
	for _, want := range []string{"Codex 账号与额度", "认证方式：ChatGPT", "计划：pro", "账号：t***@example.com"} {
		if !strings.Contains(account, want) {
			t.Fatalf("account view missing %q: %q", want, account)
		}
	}
}

func TestCodexWorkspaceCenterKeepsTrustedRootsTogether(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "/")
	development := controlReply(t, handler, "owner-1", "8")
	for _, want := range []string{"Codex 工作空间", "受信任本机目录", "Workspace"} {
		if !strings.Contains(development, want) {
			t.Fatalf("development center missing %q: %q", want, development)
		}
	}
	state, status, err := handler.controlStates.Load("owner-1")
	if err != nil || status != controlStateActive || len(state.Options) != 1 || state.Options[0].Action != actionSelectProject {
		t.Fatalf("development center state = %#v status=%v err=%v", state, status, err)
	}
}

func TestSessionPickerPaginatesAndAcceptsNaturalNavigation(t *testing.T) {
	handler, _ := newSessionHandler(t)
	for index := 1; index <= 14; index++ {
		_ = controlReply(t, handler, "owner-1", fmt.Sprintf("新建线程 线程 %02d", index))
	}

	first := controlReply(t, handler, "owner-1", "线程列表")
	for _, want := range []string{"页码：1 / 3", "匹配：14", "下一页 · 2/3", "线程 14", "线程 09"} {
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

func TestGlobalThreadBrowserAdoptsSelectionFromAnyPage(t *testing.T) {
	handler, _ := newSessionHandler(t)
	for index := 1; index <= 8; index++ {
		_ = controlReply(t, handler, "owner-1", fmt.Sprintf("新建线程 线程 %02d", index))
	}

	_ = controlReply(t, handler, "owner-1", "线程列表")
	second := controlReply(t, handler, "owner-1", "下一页")
	if !strings.Contains(second, "页码：2 / 2") || !strings.Contains(second, "线程 02") {
		t.Fatalf("second browser page = %q", second)
	}
	selected := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(selected, "已从 Codex 全局目录接管目标线程") || !strings.Contains(selected, "线程 02") {
		t.Fatalf("global selection result = %q", selected)
	}
}

func TestSessionSearchOpensSafeDetailAndCanSwitch(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建线程 登录排障")
	_ = controlReply(t, handler, "owner-1", "新建线程 发布检查")

	prompt := controlReply(t, handler, "owner-1", "搜索线程")
	if !strings.Contains(prompt, "发送名称、摘要或线程编号") {
		t.Fatalf("search prompt = %q", prompt)
	}
	results := controlReply(t, handler, "owner-1", "登录")
	for _, want := range []string{"Codex 全局搜索", "搜索：登录", "登录排障"} {
		if !strings.Contains(results, want) {
			t.Fatalf("search results missing %q: %q", want, results)
		}
	}
	switched := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(switched, "已从 Codex 全局目录接管目标线程") || !strings.Contains(switched, "登录排障") {
		t.Fatalf("search switch result = %q", switched)
	}
}

func TestGlobalThreadSelectionChangesRemoteFocus(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建线程 保留当前")
	_ = controlReply(t, handler, "owner-1", "新建线程 待归档")
	selected := controlReply(t, handler, "owner-1", "切换线程 保留当前")
	if !strings.Contains(selected, "Codex 全局搜索") || !strings.Contains(selected, "保留当前") {
		t.Fatalf("global search result = %q", selected)
	}
	adopted := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(adopted, "已从 Codex 全局目录接管目标线程") || !strings.Contains(adopted, "保留当前") {
		t.Fatalf("global focus result = %q", adopted)
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
	for _, want := range []string{"codex-link-clawbot 执行状态：运行中", "1  刷新状态", "2  codex-link-clawbot 请求队列", "3  取消当前执行"} {
		if !strings.Contains(status, want) {
			t.Fatalf("task status missing %q: %q", want, status)
		}
	}
	confirm := controlReply(t, handler, "owner-1", "3")
	if !strings.Contains(confirm, "准备取消 codex-link-clawbot 当前执行") || !strings.Contains(confirm, "1  确认取消执行") {
		t.Fatalf("task cancel confirmation = %q", confirm)
	}
	cancelled := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(cancelled, "已请求取消 codex-link-clawbot 当前执行") {
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
	if len(sent.Msg.ItemList) != 1 || sent.Msg.ItemList[0].TextItem == nil || !strings.Contains(sent.Msg.ItemList[0].TextItem.Text, "Codex 全局工作台") {
		t.Fatalf("sent menu = %#v", sent.Msg.ItemList)
	}
}

func TestSessionCompletionOffersCandidatesThenAcceptsNumber(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建线程 发布检查")
	_ = controlReply(t, handler, "owner-1", "新建线程 发布排障")
	completion := controlReply(t, handler, "owner-1", "切换线程 发布")
	for _, want := range []string{"Codex 全局搜索", "搜索：发布", "发布检查", "发布排障", "回复线程编号"} {
		if !strings.Contains(completion, want) {
			t.Fatalf("completion missing %q: %q", want, completion)
		}
	}
	switched := controlReply(t, handler, "owner-1", "1")
	if !strings.Contains(switched, "已从 Codex 全局目录接管目标线程") {
		t.Fatalf("numeric completion reply = %q", switched)
	}
}

func TestSessionCompletionPrefersAnExactTitle(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建线程 发布")
	_ = controlReply(t, handler, "owner-1", "新建线程 发布检查")
	results := controlReply(t, handler, "owner-1", "切换线程 发布")
	if !strings.Contains(results, "Codex 全局搜索") || !strings.Contains(results, "发布检查") || !strings.Contains(results, "发布") {
		t.Fatalf("global exact-title search = %q", results)
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

func TestUnknownSlashCommandsAreRejected(t *testing.T) {
	handler, _ := newSessionHandler(t)
	got := controlReply(t, handler, "owner-1", "/sessions")
	if !strings.Contains(got, "没有这个可用的 Codex 斜杠命令") || !strings.Contains(got, "可直接执行的命令") {
		t.Fatalf("unknown command reply = %q", got)
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
		{name: "first line", request: codex.ChatRequest{Text: "检查发布流程\n[codex-link-clawbot 交付物回传]\n/private/path"}, want: "检查发布流程"},
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
	preview := "用户的真实问题\n\n[codex-link-clawbot 交付物回传]\n/root/.codex-link-clawbot/turns/private/outbox"
	thread := codex.ThreadInfo{Preview: preview}
	if got := threadSearchTitle(thread); got != "用户的真实问题" {
		t.Fatalf("threadSearchTitle() = %q", got)
	}
	markerOnly := codex.ThreadInfo{Preview: "[codex-link-clawbot 入站文件]\n/private/file"}
	if got := threadSearchTitle(markerOnly); got != "未命名线程" {
		t.Fatalf("marker-only threadSearchTitle() = %q", got)
	}
}

func TestGlobalDirectoryExposesCodexThreadsIndependentOfWeChatOwner(t *testing.T) {
	handler, _ := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建线程 私有线程")
	got := controlReply(t, handler, "owner-2", "切换线程 00000001")
	if !strings.Contains(got, "Codex 全局搜索") || !strings.Contains(got, "私有线程") {
		t.Fatalf("global visibility reply = %q", got)
	}
	selected := controlReply(t, handler, "owner-2", "1")
	if !strings.Contains(selected, "已从 Codex 全局目录接管目标线程") {
		t.Fatalf("global adoption reply = %q", selected)
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
	if !strings.Contains(review, "Codex 移动审查") || !strings.Contains(review, "本轮未发现明确问题") ||
		!strings.Contains(review, "继续修复 · 当前线程") || !strings.Contains(review, "重新审查 · /review") {
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
