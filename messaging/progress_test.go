package messaging

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/weclaw/agent"
)

func TestFormatProgressEventPlan(t *testing.T) {
	got := formatProgressEvent(agent.ProgressEvent{
		Kind:      agent.ProgressPlan,
		Text:      "替换桥接服务",
		Completed: 2,
		Total:     4,
	})
	if got != "进度：已完成 2/4 个计划步骤\n当前：替换桥接服务" {
		t.Fatalf("formatProgressEvent() = %q", got)
	}
}

func TestFormatProgressEventTruncatesByRune(t *testing.T) {
	got := formatProgressEvent(agent.ProgressEvent{
		Kind: agent.ProgressCommentary,
		Text: strings.Repeat("测", 500),
	})
	if len([]rune(got)) != len([]rune("进度："))+421 {
		t.Fatalf("progress length = %d", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("progress should end with ellipsis: %q", got)
	}
}

func TestActiveTaskSummaryContainsSnapshot(t *testing.T) {
	task := &activeTask{started: time.Now().Add(-70 * time.Second), status: "正在验证链路"}
	got := task.busySummary()
	if !strings.Contains(got, "上一项任务仍在执行") || !strings.Contains(got, "正在验证链路") {
		t.Fatalf("unexpected active task summary: %q", got)
	}
}

func TestActiveTaskCancelIsIdempotent(t *testing.T) {
	task := newActiveTask(context.Background())
	if !task.requestCancel() {
		t.Fatal("first cancellation should be accepted")
	}
	if task.requestCancel() {
		t.Fatal("duplicate cancellation should be rejected")
	}
	if !task.cancelRequested() {
		t.Fatal("task should record the cancellation request")
	}
	select {
	case <-task.context().Done():
	default:
		t.Fatal("task context should be cancelled")
	}
	if got := task.statusSummary(); !strings.Contains(got, "任务状态：正在取消") {
		t.Fatalf("unexpected cancellation status: %q", got)
	}
}

func TestActiveTaskFinishAndCancelRaceHasSingleWinner(t *testing.T) {
	completed := newActiveTask(context.Background())
	if !completed.finish() {
		t.Fatal("an uncancelled task should allow its final result")
	}
	if completed.requestCancel() {
		t.Fatal("cancellation must be rejected after task completion")
	}

	cancelled := newActiveTask(context.Background())
	if !cancelled.requestCancel() {
		t.Fatal("cancellation should win while the task is active")
	}
	if cancelled.finish() {
		t.Fatal("a cancelled task must suppress its final result")
	}
}

func TestUnsentProgressSuppressesPreviouslySentDetails(t *testing.T) {
	sent := map[string]struct{}{
		"进度：正在检查服务": {},
	}

	if got, ok := unsentProgress("进度：正在检查服务", sent); ok || got != "" {
		t.Fatalf("duplicate progress should be suppressed, got %q, ok=%v", got, ok)
	}
	if got, ok := unsentProgress("进度：正在验证链路", sent); !ok || got != "进度：正在验证链路" {
		t.Fatalf("new progress should be sent, got %q, ok=%v", got, ok)
	}
	if got, ok := unsentProgress("  \n\t", sent); ok || got != "" {
		t.Fatalf("blank progress should be suppressed, got %q, ok=%v", got, ok)
	}
}
