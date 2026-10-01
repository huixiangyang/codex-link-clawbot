package wechat

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/visual"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/access"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/preference"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/workspace"
)

type numberMenuFixture struct {
	h             *testHandler
	client        *ilink.Client
	agent         *handlerThreadClient
	sequence      int64
	mu            sync.Mutex
	messages      []ilink.SendMsg
	rejectUpload  bool
	rejectMessage bool
}

func newNumberMenuFixture(t *testing.T) *numberMenuFixture {
	t.Helper()
	f := &numberMenuFixture{agent: newHandlerThreadClient()}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/sendmessage":
			var payload ilink.SendMessageRequest
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Error(err)
				return
			}
			f.mu.Lock()
			f.messages = append(f.messages, payload.Msg)
			f.mu.Unlock()
			if f.rejectMessage {
				_, _ = w.Write([]byte(`{"ret":-2}`))
				return
			}
			_, _ = w.Write([]byte(`{"ret":0}`))
		case "/ilink/bot/getuploadurl":
			if f.rejectUpload {
				_, _ = w.Write([]byte(`{"ret":-2}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ret": 0, "upload_full_url": server.URL + "/upload"})
		case "/upload":
			w.Header().Set("X-Encrypted-Param", "download-token")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	f.client = ilink.NewClient(&ilink.Credentials{BotToken: "token", ILinkBotID: "bot", ILinkUserID: "owner", BaseURL: server.URL})
	f.h = newBareHandler(f.agent)
	var err error
	f.h.projects, err = workspace.NewManager([]workspace.Definition{{ID: "alpha", Name: "主项目", Root: t.TempDir()}, {ID: "beta", Name: "第二项目", Root: t.TempDir()}})
	if err != nil {
		t.Fatal(err)
	}
	f.h.preferences, err = preference.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.h.tasks, err = request.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	attachTestSessionManager(t, f.h)
	f.h.coordinator, err = newTestCoordinator(f.h, f.h.tasks)
	if err != nil {
		t.Fatal(err)
	}
	f.h.remoteLock, err = access.NewRemoteLock(filepath.Join(t.TempDir(), "lock.json"), "123456")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = f.h.coordinator.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	runtime := &Runtime{Handler: f.h.Handler, Coordinator: f.h.coordinator}
	f.h.menus = newNumberMenus(f.h.Handler, f.agent, runtime)
	return f
}
func (f *numberMenuFixture) input(t *testing.T, text string) int64 {
	t.Helper()
	f.sequence++
	f.send(t, f.sequence, text)
	return f.sequence
}
func (f *numberMenuFixture) send(t *testing.T, id int64, text string) {
	t.Helper()
	err := f.h.HandleMessage(context.Background(), f.client, ilink.WeixinMessage{MessageID: id, FromUserID: "owner", MessageType: ilink.MessageTypeUser, MessageState: ilink.MessageStateFinish, ContextToken: "fresh-context", ItemList: []ilink.MessageItem{{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: text}}}})
	if err != nil {
		t.Fatal(err)
	}
	waitForEffects(t, f.h.coordinator)
}
func (f *numberMenuFixture) enqueue(t *testing.T, summary string) request.Task {
	t.Helper()
	intent, err := f.h.targets.Capture("owner")
	if err != nil {
		t.Fatal(err)
	}
	task, _, err := f.h.tasks.Start(request.StartInput{TargetID: intent.ID, OwnerID: "owner", ProjectID: intent.WorkspaceID, SourceMessageKey: "task:" + summary, Summary: summary, Text: summary, ResponseMode: presentation.ResponseAdaptive, VisualStyle: presentation.DefaultStyle})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestMenuEntrySymbols(t *testing.T) {
	for _, text := range []string{"0", "#0", "#1", "/help", "/tmp/project", "# 标题", "菜单", "Codex", "Codex 菜单", "codex菜单", "Codex Link", "codex-link"} {
		if isCodexLinkMenu(text) {
			t.Fatalf("non-entry text opened menu: %q", text)
		}
	}
	f := newNumberMenuFixture(t)
	for _, text := range []string{"#", "/", " \t＃\n", " ／ "} {
		f.input(t, text)
		if s := f.h.menus.session("owner"); s.location.kind != "home" || s.closed {
			t.Fatalf("entry did not open home: %q", text)
		}
		f.input(t, "#3")
		f.input(t, text)
		if f.h.menus.session("owner").location.kind != "home" || len(f.h.tasks.List("owner")) != 0 {
			t.Fatalf("entry did not return home or submitted work: %q", text)
		}
		f.input(t, "#9")
	}
}

func TestNumberMenuChangesTargetWithoutExecutingAndDeduplicates(t *testing.T) {
	f := newNumberMenuFixture(t)
	old := f.enqueue(t, "原会话请求")
	f.input(t, "#")
	id := f.input(t, "#1")
	current := f.h.targets.Current("owner")
	if current.ID == old.TargetID || current.ThreadID != "" || len(f.agent.threads) != 0 {
		t.Fatal("new conversation must prepare a separate intent without running Codex")
	}
	f.send(t, id, "#1")
	if f.h.targets.Current("owner").ID != current.ID {
		t.Fatal("duplicate input created another conversation")
	}
	f.h.menus = newNumberMenus(f.h.Handler, f.agent, &Runtime{Handler: f.h.Handler, Coordinator: f.h.coordinator})
	f.send(t, id-1, "#")
	f.send(t, id, "#1")
	if f.h.targets.Current("owner").ID != current.ID {
		t.Fatal("batch replay after restart created another conversation")
	}
	f.input(t, "#")
	f.input(t, "#3")
	s := f.h.menus.session("owner")
	selected := s.page.actions[2].workspaceID
	f.input(t, "#２")
	if f.h.targets.Current("owner").WorkspaceID != selected {
		t.Fatal("workspace number did not select its bound workspace")
	}
	queued, _ := f.h.tasks.Find("owner", old.ID)
	if queued.TargetID != old.TargetID || queued.ProjectID != old.ProjectID {
		t.Fatal("menu changed a fixed execution target")
	}
}

func TestNumberMenuPaginationUsesDisplayedThreadIDs(t *testing.T) {
	f := newNumberMenuFixture(t)
	root := f.h.projects.List()[0].Root
	for i := 0; i < 9; i++ {
		if _, err := f.agent.StartThread(context.Background(), root); err != nil {
			t.Fatal(err)
		}
	}
	f.input(t, "/")
	f.input(t, "#2")
	s := f.h.menus.session("owner")
	if len(s.page.Options) != 4 || !s.page.Next || s.page.Previous {
		t.Fatalf("bad first page: %+v", s.page)
	}
	f.input(t, "#8")
	bound := s.page.actions[1].threadID
	if !s.page.Previous || !s.page.Next {
		t.Fatal("missing pagination")
	}
	if _, err := f.agent.StartThread(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	f.input(t, "#1")
	if f.h.targets.Current("owner").ThreadID != bound {
		t.Fatal("live list insertion changed selected thread")
	}
	if len(f.h.tasks.List("owner")) != 0 {
		t.Fatal("numeric navigation was submitted to Codex")
	}
}

func TestNumberMenuExpiryExitAndUnlockDoNotLeakControlInput(t *testing.T) {
	f := newNumberMenuFixture(t)
	f.input(t, "#")
	originalTarget := f.h.targets.Current("owner")
	f.h.menus.session("owner").expires = time.Now().Add(-time.Minute)
	f.input(t, "#1")
	if f.h.menus.session("owner").location.kind != "home" || f.h.targets.Current("owner") != originalTarget {
		t.Fatal("expired selection executed")
	}
	f.input(t, "#88")
	if len(f.h.tasks.List("owner")) != 0 {
		t.Fatal("invalid menu number was executed")
	}
	f.input(t, "#9")
	f.input(t, "42")
	if len(f.h.tasks.List("owner")) != 1 {
		t.Fatal("number after explicit exit was not a normal prompt")
	}
	if err := f.h.remoteLock.Lock("owner"); err != nil {
		t.Fatal(err)
	}
	f.input(t, "#")
	f.input(t, "#1")
	f.h.menus.session("owner").expires = time.Now().Add(-time.Minute)
	f.input(t, "expired-secret")
	if len(f.h.tasks.List("owner")) != 1 {
		t.Fatal("expired unlock input leaked into requests")
	}
	f.input(t, "#1")
	f.input(t, "bad-code")
	f.input(t, "123456")
	if f.h.remoteLock.IsLocked("owner") || len(f.h.tasks.List("owner")) != 1 {
		t.Fatal("unlock failed or leaked the secret")
	}
}

type numberMenuRenderer struct {
	path          string
	fail, cleaned bool
}

func TestNumberMenuReadsPagesAndRetriesOnlyAfterConfirmation(t *testing.T) {
	f := newNumberMenuFixture(t)
	completed := f.enqueue(t, "已完成的请求")
	outbox, err := f.h.tasks.PrepareOutbox("owner", completed.ID)
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(outbox, "report.txt")
	if err := os.WriteFile(artifact, []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.tasks.FreezeResult("owner", completed.ID, request.FreezeResultInput{Reply: strings.Repeat("结果内容。", 220), ArtifactPaths: []string{artifact}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.tasks.Finish("owner", completed.ID, request.StateSucceeded, ""); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(artifact); err != nil {
		t.Fatal(err)
	}
	f.input(t, "#")
	f.input(t, "#5")
	f.input(t, "#1")
	f.input(t, "#1")
	s := f.h.menus.session("owner")
	if !s.textOnly || !strings.Contains(s.text, "结果文字 1/3") || len([]rune(s.text)) > 600 {
		t.Fatalf("unbounded reading page: %q", s.text)
	}
	f.input(t, "#8")
	if !strings.Contains(s.text, "结果文字 2/3") {
		t.Fatal("next page failed")
	}
	f.input(t, "#7")
	f.input(t, "#6")
	if s.location.kind != "task" || s.textOnly {
		t.Fatal("could not return to task details")
	}
	if err := os.WriteFile(artifact, []byte("report"), 0600); err != nil {
		t.Fatal(err)
	}
	f.input(t, "#2")
	if s.location.kind != "confirm-redeliver" {
		t.Fatal("redelivery missing confirmation")
	}
	id := f.input(t, "#1")
	result, err := f.h.tasks.LoadResult("owner", completed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Attempts) != 1 || result.Receipt.Outcome != request.DeliverySucceeded {
		t.Fatalf("redelivery: %+v", result.Receipt)
	}
	f.send(t, id, "#1")
	result, err = f.h.tasks.LoadResult("owner", completed.ID)
	if err != nil || len(result.Attempts) != 1 {
		t.Fatal("duplicate confirmation resent result")
	}
	failed := f.enqueue(t, "失败的请求")
	if _, err := f.h.tasks.Finish("owner", failed.ID, request.StateFailed, request.ReasonCodexFailed); err != nil {
		t.Fatal(err)
	}
	f.input(t, "#")
	f.input(t, "#5")
	for n, a := range s.page.actions {
		if a.taskID == failed.ID {
			f.input(t, "#"+fmt.Sprint(n))
			break
		}
	}
	f.input(t, "#3")
	if len(f.h.tasks.List("owner")) != 2 || s.location.kind != "confirm-retry" {
		t.Fatal("retry happened before confirmation")
	}
	id = f.input(t, "#1")
	f.send(t, id, "#1")
	tasks := f.h.tasks.List("owner")
	if len(tasks) != 3 {
		t.Fatalf("retry created %d tasks", len(tasks))
	}
	count := 0
	for _, task := range tasks {
		if task.RetryOf == failed.ID {
			count++
		}
	}
	if count != 1 {
		t.Fatal("retry did not link exactly one new request")
	}
}

func TestNumberMenuSettingsAndRestartUseSafeDefaults(t *testing.T) {
	f := newNumberMenuFixture(t)
	f.input(t, "#")
	f.input(t, "#6")
	f.input(t, "#1")
	s := f.h.menus.session("owner")
	if _, ok := s.page.actions[3]; ok {
		t.Fatal("reading offered without renderer")
	}
	f.h.visual = &numberMenuRenderer{fail: true}
	f.input(t, "#")
	f.input(t, "#6")
	f.input(t, "#1")
	f.input(t, "#3")
	if f.h.preferences.Get("owner").ResponseMode != presentation.ResponseReading {
		t.Fatal("reading setting was not saved")
	}
	f.input(t, "#")
	f.input(t, "#6")
	f.input(t, "#2")
	f.input(t, "#5")
	if f.h.preferences.Get("owner").Style != presentation.StyleMinimal {
		t.Fatal("style was not saved")
	}
	originalTarget := f.h.targets.Current("owner")
	f.h.menus = newNumberMenus(f.h.Handler, f.agent, &Runtime{Handler: f.h.Handler, Coordinator: f.h.coordinator})
	f.input(t, "#1")
	if f.h.targets.Current("owner") != originalTarget || f.h.menus.session("owner").location.kind != "home" {
		t.Fatal("stale number after restart executed")
	}
}

func (r *numberMenuRenderer) RenderDocument(context.Context, visual.Document) (*visual.Artifact, error) {
	return nil, errors.New("unexpected card")
}
func (r *numberMenuRenderer) RenderMenu(context.Context, visual.Menu) (*visual.Artifact, error) {
	a := &visual.Artifact{Path: r.path, Cleanup: func() { r.cleaned = true }}
	if r.fail {
		return a, errors.New("render failed")
	}
	return a, nil
}
func TestNumberMenuSendsOneImageOrNumberedFallback(t *testing.T) {
	for _, scenario := range []string{"image", "render-failed", "upload-failed", "disabled"} {
		t.Run(scenario, func(t *testing.T) {
			f := newNumberMenuFixture(t)
			path := filepath.Join(t.TempDir(), "menu.png")
			if err := os.WriteFile(path, testPNG(t), 0o600); err != nil {
				t.Fatal(err)
			}
			renderer := &numberMenuRenderer{path: path, fail: scenario == "render-failed"}
			if scenario != "disabled" {
				f.h.visual = renderer
			}
			f.rejectUpload = scenario == "upload-failed"
			f.input(t, "#")
			f.mu.Lock()
			defer f.mu.Unlock()
			if len(f.messages) != 1 {
				t.Fatalf("menu sent %d messages instead of one", len(f.messages))
			}
			item := f.messages[0].ItemList[0]
			if scenario == "image" {
				if item.Type != ilink.ItemTypeImage {
					t.Fatal("expected an image")
				}
			} else if item.TextItem == nil || !strings.Contains(item.TextItem.Text, "1 新建对话") || !strings.Contains(item.TextItem.Text, "#9 退出") {
				t.Fatal("fallback lost numbered actions")
			}
			if scenario != "disabled" && !renderer.cleaned {
				t.Fatal("render artifact leaked")
			}
		})
	}
}
