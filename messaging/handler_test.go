package messaging

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/huixiangyang/weclaw/ilink"
	"github.com/huixiangyang/weclaw/preference"
	"github.com/huixiangyang/weclaw/taskqueue"
	"github.com/huixiangyang/weclaw/visual"
)

func newTestHandler() *Handler {
	return NewHandler(nil)
}

func TestRuntimeCenterShowsBridgeAndCodexIdentity(t *testing.T) {
	handler, _ := newSessionHandler(t)
	handler.SetBridgeInfo("v1.4.0-runtime.1", "127.0.0.1:18011")
	handler.startedAt = time.Now().Add(-2*time.Hour - 5*time.Minute)
	status := controlReply(t, handler, "owner-1", "运行中心")
	for _, want := range []string{
		"运行中心", "WeClaw：运行中", "版本：v1.4.0-runtime.1", "已运行：2 小时 5 分",
		"本地接口：127.0.0.1:18011", "Codex：运行中", "协议：App Server",
		"模型：使用 Codex 默认配置", "项目目录：/workspace", "Codex PID：4242",
		"1  项目中心", "2  刷新运行中心",
	} {
		if !strings.Contains(status, want) {
			t.Fatalf("runtime center missing %q: %q", want, status)
		}
	}
}

func TestFormatUptimeUsesMobileFriendlyUnits(t *testing.T) {
	tests := []struct {
		elapsed time.Duration
		want    string
	}{
		{elapsed: 31 * time.Second, want: "31 秒"},
		{elapsed: 3*time.Hour + 8*time.Minute, want: "3 小时 8 分"},
		{elapsed: 26*time.Hour + 10*time.Minute, want: "1 天 2 小时"},
	}
	for _, test := range tests {
		if got := formatUptime(test.elapsed); got != test.want {
			t.Fatalf("formatUptime(%s) = %q, want %q", test.elapsed, got, test.want)
		}
	}
}

func TestControlGuideKeepsOnlyMenuEntry(t *testing.T) {
	text := controlGuide()
	if text == "" {
		t.Error("guide text is empty")
	}
	if !strings.Contains(text, "发送 / 打开操作菜单") {
		t.Error("guide should mention the single menu entry")
	}
	if !strings.Contains(text, "发送“取消”") {
		t.Error("guide should mention natural-language cancellation")
	}
	if !strings.Contains(text, "发送“视觉风格”") {
		t.Error("guide should mention visual style selection")
	}
	if strings.Contains(text, "/status") || strings.Contains(text, "/session") {
		t.Error("guide must not expose legacy slash commands")
	}
}

func TestTaskControlStatusAndCancel(t *testing.T) {
	h, cancel := testHandlerWithRunningTask(t, "user-1")
	defer cancel()
	if got := h.buildTaskStatus("user-1"); !strings.Contains(got, "任务状态：运行中") {
		t.Fatalf("unexpected active status: %q", got)
	}
	if got := h.cancelActiveTask("user-1"); !strings.Contains(got, "已请求取消当前任务") {
		t.Fatalf("unexpected cancellation result: %q", got)
	}
	if got := h.cancelActiveTask("user-1"); !strings.Contains(got, "正在取消") {
		t.Fatalf("unexpected duplicate cancellation result: %q", got)
	}
}

func TestNaturalTaskControlsAcceptCommonPunctuation(t *testing.T) {
	h, cancel := testHandlerWithRunningTask(t, "user-1")
	defer cancel()

	status, handled := h.handleControlInput(context.Background(), "user-1", "状态？", false)
	if !handled || !strings.Contains(status, "任务状态：运行中") {
		t.Fatalf("natural status = %q, handled=%v", status, handled)
	}
	cancelled, handled := h.handleControlInput(context.Background(), "user-1", "取消！", false)
	if !handled || !strings.Contains(cancelled, "已请求取消当前任务") {
		t.Fatalf("natural cancellation = %q, handled=%v", cancelled, handled)
	}
}

func testHandlerWithRunningTask(t *testing.T, ownerID string) (*Handler, context.CancelFunc) {
	t.Helper()
	store, err := taskqueue.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	queued, _, err := store.Enqueue(taskqueue.EnqueueInput{
		SourceMessageKey: "source-running", OwnerID: ownerID, ProjectID: "project", Summary: "运行测试", Text: "执行测试",
		ResponseMode: preference.ResponseAdaptive, VisualStyle: visual.StyleEditorial,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, claimed, err := store.ClaimNext(nil); err != nil || !claimed {
		t.Fatalf("claim running task: claimed=%v err=%v", claimed, err)
	}
	handler := newTestHandler()
	taskContext, cancel := context.WithCancel(context.Background())
	handler.tasks = store
	handler.coordinator = &Coordinator{
		handler: handler, tasks: store, wake: make(chan struct{}, 1),
		active: &coordinatorTask{ownerID: ownerID, taskID: queued.ID, cancel: cancel},
	}
	return handler, func() {
		cancel()
		_ = taskContext
	}
}

func TestExtractImagesReturnsEveryImageItem(t *testing.T) {
	first := &ilink.ImageItem{URL: "https://example.com/one.png"}
	second := &ilink.ImageItem{URL: "https://example.com/two.png"}
	got := extractImages(ilink.WeixinMessage{ItemList: []ilink.MessageItem{
		{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "分析图片"}},
		{Type: ilink.ItemTypeImage, ImageItem: first},
		{Type: ilink.ItemTypeImage, ImageItem: second},
	}})
	if len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("extractImages() = %#v", got)
	}
}

func TestExtractFilesReturnsEveryFileItem(t *testing.T) {
	first := &ilink.FileItem{FileName: "report.pdf"}
	second := &ilink.FileItem{FileName: "source.zip"}
	got := extractFiles(ilink.WeixinMessage{ItemList: []ilink.MessageItem{
		{Type: ilink.ItemTypeText, TextItem: &ilink.TextItem{Text: "检查"}},
		{Type: ilink.ItemTypeFile, FileItem: first},
		{Type: ilink.ItemTypeFile, FileItem: second},
	}})
	if len(got) != 2 || got[0] != first || got[1] != second {
		t.Fatalf("extractFiles() = %#v", got)
	}
}
