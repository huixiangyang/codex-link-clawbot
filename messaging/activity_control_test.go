package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/huixiangyang/weclaw/ilink"
)

func TestActivityManagementPaginatesAndPreservesDetailPage(t *testing.T) {
	handler, _ := newSessionHandler(t)
	store, err := NewActivityStore(filepath.Join(t.TempDir(), "task-history.json"))
	if err != nil {
		t.Fatal(err)
	}
	current := time.Unix(1_700_000_000, 0)
	store.now = func() time.Time { return current }
	for index := 1; index <= 8; index++ {
		id, startErr := store.Start("owner-1", fmt.Sprintf("检查任务 %02d", index))
		if startErr != nil {
			t.Fatal(startErr)
		}
		current = current.Add(time.Duration(index) * time.Second)
		status := ActivitySucceeded
		if index == 3 {
			status = ActivityFailed
		}
		if finishErr := store.Finish("owner-1", id, status); finishErr != nil {
			t.Fatal(finishErr)
		}
		current = current.Add(time.Second)
	}
	handler.SetActivityStore(store)

	main := controlReply(t, handler, "owner-1", "/")
	if !strings.Contains(main, "3  任务记录") {
		t.Fatalf("main activity entry = %q", main)
	}
	first := controlReply(t, handler, "owner-1", "任务记录")
	for _, want := range []string{"页码：1 / 2", "记录：8", "完成：7", "异常：1", "下一页 · 2/2"} {
		if !strings.Contains(first, want) {
			t.Fatalf("activity first page missing %q: %q", want, first)
		}
	}
	second := controlReply(t, handler, "owner-1", "下一页")
	if !strings.Contains(second, "页码：2 / 2") || !strings.Contains(second, "上一页 · 1/2") {
		t.Fatalf("activity second page = %q", second)
	}
	detail := controlReply(t, handler, "owner-1", "1")
	for _, want := range []string{"任务详情", "摘要：检查任务", "状态：已完成", "开始：", "结束：", "用时："} {
		if !strings.Contains(detail, want) {
			t.Fatalf("activity detail missing %q: %q", want, detail)
		}
	}
	back := controlReply(t, handler, "owner-1", "0")
	if !strings.Contains(back, "页码：2 / 2") {
		t.Fatalf("activity detail lost its source page: %q", back)
	}
}

func TestCodexTurnWritesSuccessfulActivityRecord(t *testing.T) {
	var sent ilink.SendMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" {
			http.NotFound(w, r)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&sent); err != nil {
			t.Errorf("decode task reply: %v", err)
		}
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer server.Close()

	handler, _ := newSessionHandler(t)
	store, err := NewActivityStore(filepath.Join(t.TempDir(), "task-history.json"))
	if err != nil {
		t.Fatal(err)
	}
	handler.SetActivityStore(store)
	client := ilink.NewClient(&ilink.Credentials{
		BotToken: "token", ILinkBotID: "bot-1", ILinkUserID: "owner-1", BaseURL: server.URL,
	})
	handler.sendToCodex(context.Background(), client, ilink.WeixinMessage{
		FromUserID: "owner-1", ContextToken: "activity-context",
	}, "检查发布流程", nil, nil, "activity-client")
	records := store.List("owner-1")
	if len(records) != 1 || records[0].Summary != "检查发布流程" || records[0].Status != ActivitySucceeded || records[0].FinishedAt == 0 {
		t.Fatalf("turn activity records = %#v", records)
	}
	if len(sent.Msg.ItemList) != 1 || sent.Msg.ItemList[0].TextItem == nil {
		t.Fatalf("turn reply = %#v", sent.Msg.ItemList)
	}
}

func TestTaskActivitySummaryNeverIncludesAttachmentPaths(t *testing.T) {
	if got := taskActivitySummary("分析 /root/private/report.log 和 C:\\secret\\report.pdf\n/private/inbox/file", 2, 1); got != "分析 [本机路径] 和 [本机路径] · 2 张图片 · 1 个文件" {
		t.Fatalf("taskActivitySummary() = %q", got)
	}
	if got := taskActivitySummary("", 1, 2); got != "附件分析 · 1 张图片 · 2 个文件" {
		t.Fatalf("attachment-only summary = %q", got)
	}
	if got := taskActivitySummary("分析 https://example.com/private/report", 0, 0); got != "分析 [链接]" {
		t.Fatalf("URL summary = %q", got)
	}
}
