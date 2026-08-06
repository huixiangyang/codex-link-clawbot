package messaging

import (
	"strings"
	"testing"

	"github.com/huixiangyang/weclaw/internal/preference"
	"github.com/huixiangyang/weclaw/internal/taskqueue"
	"github.com/huixiangyang/weclaw/internal/visual"
)

func TestTaskCenterUsesPersistentQueueAndKeepsDetailPage(t *testing.T) {
	handler, _ := newSessionHandler(t)
	store, err := taskqueue.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler.tasks = store
	for index := 0; index < 8; index++ {
		_, _, err := store.Enqueue(taskqueue.EnqueueInput{
			SourceMessageKey: "source-" + string(rune('a'+index)), OwnerID: "owner-1", ProjectID: "project",
			Summary: "检查任务", Text: "执行检查", ResponseMode: preference.ResponseAdaptive, VisualStyle: visual.StyleEditorial,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	first := handler.openActivities("owner-1", 1)
	for _, want := range []string{"WeClaw 请求队列", "页码：1 / 2", "等待：8", "下一页 · 2/2"} {
		if !strings.Contains(first, want) {
			t.Fatalf("task first page missing %q: %q", want, first)
		}
	}
	second := handler.openActivities("owner-1", 2)
	if !strings.Contains(second, "页码：2 / 2") {
		t.Fatalf("task second page = %q", second)
	}
	tasks := store.List("owner-1")
	detail := handler.openActivityDetail("owner-1", tasks[6].ID, 2)
	for _, want := range []string{"WeClaw 执行记录", "摘要：检查任务", "状态：排队中", "编号："} {
		if !strings.Contains(detail, want) {
			t.Fatalf("task detail missing %q: %q", want, detail)
		}
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

func TestTaskCenterOnlyOffersFrozenRecoveryWhenResultExists(t *testing.T) {
	handler, _ := newSessionHandler(t)
	store, err := taskqueue.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler.tasks = store
	enqueue := func(source string) taskqueue.Task {
		t.Helper()
		_, _, err := store.Enqueue(taskqueue.EnqueueInput{
			SourceMessageKey: source, OwnerID: "owner-1", ProjectID: "project", Summary: "交付测试", Text: "执行",
			ResponseMode: preference.ResponseAdaptive, VisualStyle: visual.StyleEditorial,
		})
		if err != nil {
			t.Fatal(err)
		}
		task, claimed, err := store.ClaimNext(nil)
		if err != nil || !claimed {
			t.Fatalf("ClaimNext() = %#v, %v, %v", task, claimed, err)
		}
		return task
	}

	withoutResult := enqueue("source-no-result")
	if _, err := store.Finish("owner-1", withoutResult.ID, taskqueue.StateFailed, taskqueue.ReasonDeliveryFailed); err != nil {
		t.Fatal(err)
	}
	detail := handler.openActivityDetail("owner-1", withoutResult.ID, 1)
	if !strings.Contains(detail, "重试请求") || strings.Contains(detail, "取回冻结文字") {
		t.Fatalf("detail without result = %q", detail)
	}

	withResult := enqueue("source-with-result")
	if _, err := store.FreezeResult("owner-1", withResult.ID, taskqueue.FreezeResultInput{Reply: "冻结回答"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.BeginDelivery("owner-1", withResult.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finish("owner-1", withResult.ID, taskqueue.StateFailed, taskqueue.ReasonDeliveryFailed); err != nil {
		t.Fatal(err)
	}
	detail = handler.openActivityDetail("owner-1", withResult.ID, 1)
	if !strings.Contains(detail, "取回冻结文字") || strings.Contains(detail, "重试请求") {
		t.Fatalf("detail with result = %q", detail)
	}
}
