package messaging

import (
	"strings"
	"testing"

	"github.com/huixiangyang/weclaw/internal/codex"
)

func TestFormatProgressEventPlan(t *testing.T) {
	got := formatProgressEvent(codex.ProgressEvent{
		Kind:      codex.ProgressPlan,
		Text:      "替换桥接服务",
		Completed: 2,
		Total:     4,
	})
	if got != "进度：已完成 2/4 个计划步骤\n当前：替换桥接服务" {
		t.Fatalf("formatProgressEvent() = %q", got)
	}
}

func TestFormatProgressEventTruncatesByRune(t *testing.T) {
	got := formatProgressEvent(codex.ProgressEvent{
		Kind: codex.ProgressCommentary,
		Text: strings.Repeat("测", 500),
	})
	if len([]rune(got)) != len([]rune("进度："))+421 {
		t.Fatalf("progress length = %d", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("progress should end with ellipsis: %q", got)
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
