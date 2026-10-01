package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/access"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/conversation"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/runtimecontrol"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/target"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/thread"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/workspace"
)

type workbenchCodex struct {
	codex.ThreadClient
	mu       sync.Mutex
	root     string
	items    map[string]codex.ThreadInfo
	archived map[string]bool
	next     int
}

func (c *workbenchCodex) ReadThread(_ context.Context, id string) (codex.ThreadInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	i, ok := c.items[id]
	if !ok {
		return i, fmt.Errorf("not found")
	}
	return i, nil
}
func (c *workbenchCodex) ListThreads(_ context.Context, opts codex.ThreadListOptions) (codex.ThreadPage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := codex.ThreadPage{}
	for id, i := range c.items {
		if c.archived[id] == opts.Archived && strings.Contains(i.Name, opts.SearchTerm) {
			out.Threads = append(out.Threads, i)
		}
	}
	return out, nil
}
func (c *workbenchCodex) StartThread(_ context.Context, root string) (codex.ThreadInfo, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.next++
	id := fmt.Sprintf("019fcc03-fc8b-7842-a812-%012d", c.next)
	i := codex.ThreadInfo{ID: id, Name: fmt.Sprintf("产品迭代 %02d", c.next), Cwd: root, CreatedAt: time.Now().Unix(), UpdatedAt: int64(c.next), Status: codex.ThreadStatus{Type: "idle"}}
	c.items[id] = i
	return i, nil
}
func (c *workbenchCodex) ResumeThread(ctx context.Context, id, root string) (codex.ThreadInfo, error) {
	return c.ReadThread(ctx, id)
}

func (c *workbenchCodex) SetThreadName(_ context.Context, id, name string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	i, ok := c.items[id]
	if !ok {
		return fmt.Errorf("missing")
	}
	i.Name = name
	c.items[id] = i
	return nil
}
func (c *workbenchCodex) ArchiveThread(_ context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.archived[id] = true
	return nil
}

type workbenchControl struct {
	store *request.Store
	mu    sync.Mutex
}

func (c *workbenchControl) Cancel(owner, id string) bool {
	task, ok := c.store.Find(owner, id)
	if !ok || task.State != request.StateRunning {
		return false
	}
	_, err := c.store.Finish(owner, id, request.StateCancelled, request.ReasonUserCancelled)
	return err == nil
}
func (c *workbenchControl) WithIdleSession(owner, thread string, action func() error) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, busy := c.store.Active(owner, "", thread); busy {
		return request.ErrSessionBusy
	}
	return action()
}
func (c *workbenchControl) Retry(_ context.Context, owner, id, op string) (request.Task, error) {
	source, err := request.RecoverySource(owner, op)
	if err != nil {
		return request.Task{}, err
	}
	return c.store.Retry(owner, id, source, "fixture-context")
}
func (c *workbenchControl) Redeliver(_ context.Context, owner, id, op string) error {
	result, duplicate, err := c.store.BeginRedelivery(owner, id, op)
	if err != nil || duplicate {
		return err
	}
	return c.store.RecordDelivery(owner, id, request.DeliveryReceipt{OperationID: op, Outcome: request.DeliverySucceeded, AttemptedAt: time.Now().Unix(), TextSent: result.Reply != ""})
}
func (c *workbenchControl) RestoreResult(_ context.Context, owner, id string) error {
	checkpoint, err := c.store.LoadCompletion(owner, id)
	if err != nil {
		return err
	}
	_, err = c.store.FreezeResult(owner, id, request.FreezeResultInput{Reply: checkpoint.Reply})
	return err
}
func workbenchFixture(t testing.TB) (*ConsoleServer, *request.Store, string) {
	t.Helper()
	root := t.TempDir()
	check := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	ws, err := workspace.NewManager([]workspace.Definition{{ID: "alpha", Name: "主工作空间", Root: root}, {ID: "beta", Name: "实验空间", Root: root}})
	check(err)
	targets, err := target.Open(root, "alpha")
	check(err)
	threads, err := thread.NewManager()
	check(err)
	client := &workbenchCodex{root: root, items: map[string]codex.ThreadInfo{}, archived: map[string]bool{}}
	var first codex.ThreadInfo
	for i := 0; i < 26; i++ {
		item, err := client.StartThread(context.Background(), root)
		check(err)
		if i == 0 {
			first = item
		}
	}
	_, err = targets.SelectThread("owner", "alpha", first.ID, targets.Snapshot("owner"))
	check(err)
	store, err := request.NewStore(root)
	check(err)
	resultID := ""
	for i := 0; i < 16; i++ {
		task, _, err := store.Start(request.StartInput{SourceMessageKey: fmt.Sprint("source-", i), OwnerID: "owner", ProjectID: "alpha", TargetID: targets.Current("owner").ID, ThreadID: first.ID, Summary: fmt.Sprintf("梳理开发流程与交付结果 · %02d", i+1), Text: "分析现有架构，梳理业务边界，给出可实施的重构方案。", ContextToken: "private-fixture-context", ResponseMode: presentation.ResponseAdaptive, VisualStyle: presentation.DefaultStyle})
		check(err)
		if i == 15 {
			_, err = store.Finish("owner", task.ID, request.StateFailed, request.ReasonCodexFailed)
			check(err)
			continue
		}
		if i == 13 {
			check(store.CompleteExecution("owner", task.ID, "已完成的成果，恢复保存后可以取回。"))
			_, err = store.Finish("owner", task.ID, request.StateFailed, request.ReasonResultFreezeFailed)
			check(err)
			continue
		}
		outbox, err := store.PrepareOutbox("owner", task.ID)
		check(err)
		artifact := filepath.Join(outbox, "架构分析.md")
		check(os.WriteFile(artifact, []byte("# 交付物\n测试用架构分析"), 0600))
		_, err = store.FreezeResult("owner", task.ID, request.FreezeResultInput{Reply: "工作已经完成。\n\n一、统一请求与结果的生命周期\n将执行、结果存储和微信投递分别记录，让每次工作都有明确的归属和可追溯的交付。\n\n二、持续同一段对话\n请求在提交时冻结会话意图，之后的消息自然延续上下文。切换工作空间只影响未来提交的请求。\n\n三、明确恢复路径\n执行失败时可以显式重跑；投递失败时取回已有结果，无需重复执行。", ArtifactPaths: []string{artifact}})
		check(err)
		_, err = store.BeginDelivery("owner", task.ID)
		check(err)
		outcome := request.DeliverySucceeded
		failure := ""
		if i == 14 {
			outcome = request.DeliveryExplicitFailure
			failure = request.ReasonDeliveryFailed
		}
		check(store.RecordDelivery("owner", task.ID, request.DeliveryReceipt{Outcome: outcome, AttemptedAt: time.Now().Unix(), TextSent: true, FailureCode: failure}))
		_, err = store.Finish("owner", task.ID, request.StateSucceeded, "")
		check(err)
		resultID = task.ID
	}
	prefs, err := preference.NewStore(root)
	check(err)
	lock, err := access.NewRemoteLock(root, "fixture-unlock")
	check(err)
	runtime := runtimecontrol.New("test-workbench", store, nil)
	runtime.SetCodexReady(true)
	runtime.SetReady()
	control := &workbenchControl{store: store}
	conversations, err := conversation.New(conversation.Dependencies{Workspaces: ws, Threads: threads, Targets: targets, Requests: store, Client: client, Control: control})
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewConsoleServer("127.0.0.1:0", strings.Repeat("a", 64), ConsoleDependencies{Runtime: runtime, Workspaces: ws, Threads: threads, Targets: targets, Requests: store, Preferences: prefs, RemoteLock: lock, Codex: client, Conversations: conversations, Recovery: control, OwnerID: "owner"})
	check(err)
	return server, store, resultID
}
func TestWorkbenchResultRecoveryAndIsolation(t *testing.T) {
	server, store, id := workbenchFixture(t)
	handler := server.securityHeaders(server.handler())
	call := func(method, path, body, token string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("X-Codex-Link-Token", token)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	token := server.token
	if w := call("GET", "/api/requests/"+id, "", ""); w.Code != 401 {
		t.Fatalf("unauthenticated detail: %d", w.Code)
	}
	detail := call("GET", "/api/requests/"+id, "", token)
	if detail.Code != 200 || !strings.Contains(detail.Body.String(), "架构分析.md") || strings.Contains(detail.Body.String(), "private-fixture-context") || strings.Contains(detail.Body.String(), store.Root()) {
		t.Fatalf("detail exposed private payload or missing result: %s", detail.Body)
	}
	if w := call("GET", "/api/requests/"+id+"/artifacts/0", "", token); w.Code != 200 || !strings.Contains(w.Body.String(), "交付物") || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment;") {
		t.Fatalf("download: %d %s", w.Code, w.Body)
	}
	if w := call("GET", "/api/requests/"+id+"/artifacts/0", "", ""); w.Code != 401 {
		t.Fatal("unauthenticated download")
	}
	foreign, _, err := store.Start(request.StartInput{OwnerID: "foreign", SourceMessageKey: "foreign", ProjectID: "alpha", Summary: "private", Text: "private", ResponseMode: presentation.ResponseAdaptive, VisualStyle: presentation.DefaultStyle})
	if err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "/api/requests/"+foreign.ID, "", token); w.Code != 404 {
		t.Fatal("foreign task visible")
	}
	foreignOutput, err := store.PrepareOutbox("foreign", foreign.ID)
	if err != nil {
		t.Fatal(err)
	}
	foreignFile := filepath.Join(foreignOutput, "foreign.txt")
	if err := os.WriteFile(foreignFile, []byte("private"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FreezeResult("foreign", foreign.ID, request.FreezeResultInput{ArtifactPaths: []string{foreignFile}}); err != nil {
		t.Fatal(err)
	}
	if w := call("GET", "/api/artifacts", "", ""); w.Code != 401 {
		t.Fatal("unauthenticated artifact catalogue")
	}
	if w := call("GET", "/api/artifacts?page_size=1", "", token); w.Code != 200 || !strings.Contains(w.Body.String(), "架构分析.md") || strings.Contains(w.Body.String(), "foreign.txt") || strings.Contains(w.Body.String(), store.Root()) {
		t.Fatalf("artifact catalogue missing result or exposed foreign/private data: %s", w.Body)
	}
	for _, page := range []int{1, 2, 999} {
		w := call("GET", fmt.Sprintf("/api/artifacts?page=%d&page_size=12", page), "", token)
		var catalogue request.ArtifactPage
		if err := json.Unmarshal(w.Body.Bytes(), &catalogue); err != nil {
			t.Fatal(err)
		}
		expectedSize := 2
		if page == 1 {
			expectedSize = 12
		}
		if w.Code != 200 || catalogue.Total != 14 || catalogue.Pages != 2 || catalogue.Page != min(page, 2) || len(catalogue.Items) != expectedSize {
			t.Fatalf("catalogue pagination: %+v", catalogue)
		}
		for _, artifact := range catalogue.Items {
			if artifact.OwnerID != "owner" {
				t.Fatal("foreign artifact in page")
			}
		}
	}
	w := call("GET", "/api/requests?view=attention", "", token)
	var requests struct {
		Total  int            `json:"total"`
		Counts map[string]int `json:"counts"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &requests); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || requests.Total != 3 || requests.Counts["all"] != 16 || requests.Counts["history"] != 13 {
		t.Fatalf("batch request classification or isolation changed: %s", w.Body)
	}
	before := server.deps.Preferences.Get("owner")
	for _, body := range []string{`{"response_mode":"voice","style":"noir"}`, `{"response_mode":"adaptive","style":"invalid"}`} {
		if w := call("PUT", "/api/preferences", body, token); w.Code < 400 {
			t.Fatal("invalid preferences saved")
		}
		if server.deps.Preferences.Get("owner") != before {
			t.Fatal("partial preference update")
		}
	}
	if w := call("GET", "/api/requests?page=2", "", token); w.Code != 200 || !strings.Contains(w.Body.String(), `"page":2`) {
		t.Fatalf("pagination: %s", w.Body)
	}
	current := server.deps.Targets.Current("owner")
	if w := call("PUT", "/api/target", `{"workspace_id":"beta","thread_id":"missing"}`, token); w.Code != 409 || server.deps.Targets.Current("owner") != current {
		t.Fatal("failed switch changed target")
	}
	snapshot := call("GET", "/api/snapshot", "", token)
	if !strings.Contains(snapshot.Body.String(), "产品迭代 01") {
		t.Fatal("current target outside first page disappeared")
	}
	var failed request.Task
	for _, task := range store.List("owner") {
		if task.State == request.StateFailed {
			failed = task
		}
	}
	body := `{"action":"retry","operation_id":"a62bbf13-6691-4f11-899c-b398fb16307f"}`
	first := call("POST", "/api/requests/"+failed.ID+"/actions", body, token)
	again := call("POST", "/api/requests/"+failed.ID+"/actions", body, token)
	if first.Code != 201 || again.Code != 201 || first.Body.String() != again.Body.String() {
		t.Fatalf("retry not idempotent: %s / %s", first.Body, again.Body)
	}
	if snapshot := call("GET", "/api/snapshot", "", token); strings.Contains(snapshot.Body.String(), `"queue"`) || strings.Contains(snapshot.Body.String(), `"queued"`) {
		t.Fatal("queue remains in API")
	}
	if w := call("POST", "/api/queue", `{"paused":true}`, token); w.Code < 400 {
		t.Fatal("retired queue endpoint accepted mutation")
	}
	busy := call("POST", "/api/requests/"+failed.ID+"/actions", `{"action":"retry","operation_id":"23611a80-f14e-4826-93d3-205f21fe91c0"}`, token)
	if busy.Code != 409 || !strings.Contains(busy.Body.String(), `"code":"session_busy"`) || !strings.Contains(busy.Body.String(), `"session":`) {
		t.Fatalf("busy retry: %d %s", busy.Code, busy.Body)
	}
	newSession := call("POST", "/api/conversations", `{"workspace_id":"alpha","name":"执行中可新建"}`, token)
	if newSession.Code != 201 {
		t.Fatalf("busy session blocked creating another: %s", newSession.Body)
	}
	if w := call("PUT", "/api/target", `{"workspace_id":"alpha","thread_id":"`+failed.ThreadID+`"}`, token); w.Code != 200 {
		t.Fatalf("active thread cannot be selected: %s", w.Body)
	}

	var started struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if w := call("POST", "/api/requests/"+started.ID+"/actions", `{"action":"cancel"}`, token); w.Code != 200 {
		t.Fatal("active execution could not be interrupted")
	}
	replay := call("POST", "/api/requests/"+failed.ID+"/actions", `{"action":"retry","operation_id":"23611a80-f14e-4826-93d3-205f21fe91c0"}`, token)
	if replay.Code != 409 || !strings.Contains(replay.Body.String(), "已经被拒绝") || len(store.List("owner")) != 17 {
		t.Fatalf("rejected operation ran after session became idle: %s", replay.Body)
	}

}

// TestBrowserWorkbench 提供隔离的真实 HTTP/API 夹具，供本地无头浏览器验收。
// 默认跳过；不会读取用户配置、连接微信或运行 Codex。
func TestBrowserWorkbench(t *testing.T) {
	ready := os.Getenv("CLAWBOT_BROWSER_FIXTURE")
	if ready == "" {
		t.Skip("opt-in browser fixture")
	}
	server, _, _ := workbenchFixture(t)
	httpServer := httptest.NewServer(server.securityHeaders(server.handler()))
	defer httpServer.Close()
	data, _ := json.Marshal(map[string]string{"url": httpServer.URL, "token": server.token})
	if err := os.WriteFile(ready, data, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(ready)
	deadline := time.NewTimer(5 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("browser fixture timed out")
		case <-ticker.C:
			if _, err := os.Stat(ready + ".stop"); err == nil {
				return
			}
		}
	}
}
