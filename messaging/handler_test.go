package messaging

import (
	"context"
	"strings"
	"testing"

	"github.com/huixiangyang/weclaw/ilink"
)

func newTestHandler() *Handler {
	return NewHandler(nil)
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
	if strings.Contains(text, "/status") || strings.Contains(text, "/session") {
		t.Error("guide must not expose legacy slash commands")
	}
}

func TestTaskControlStatusAndCancel(t *testing.T) {
	h := newTestHandler()
	if got := h.buildTaskStatus("user-1"); !strings.Contains(got, "任务状态：空闲") {
		t.Fatalf("unexpected idle status: %q", got)
	}
	if got := h.cancelActiveTask("user-1"); got != "当前没有正在执行的任务。" {
		t.Fatalf("unexpected idle cancellation: %q", got)
	}

	task := newActiveTask(context.Background())
	h.activeTasks.Store("user-1", task)
	if got := h.buildTaskStatus("user-1"); !strings.Contains(got, "任务状态：运行中") {
		t.Fatalf("unexpected active status: %q", got)
	}
	if got := h.cancelActiveTask("user-1"); !strings.Contains(got, "已请求取消当前任务") {
		t.Fatalf("unexpected cancellation result: %q", got)
	}
	if got := h.cancelActiveTask("user-1"); got != "当前任务正在取消，请稍候。" {
		t.Fatalf("unexpected duplicate cancellation result: %q", got)
	}
}

func TestNaturalTaskControlsAcceptCommonPunctuation(t *testing.T) {
	h := newTestHandler()
	task := newActiveTask(context.Background())
	h.activeTasks.Store("user-1", task)

	status, handled := h.handleControlInput(context.Background(), "user-1", "状态？", false)
	if !handled || !strings.Contains(status, "任务状态：运行中") {
		t.Fatalf("natural status = %q, handled=%v", status, handled)
	}
	cancelled, handled := h.handleControlInput(context.Background(), "user-1", "取消！", false)
	if !handled || !strings.Contains(cancelled, "已请求取消当前任务") {
		t.Fatalf("natural cancellation = %q, handled=%v", cancelled, handled)
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
