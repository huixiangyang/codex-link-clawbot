package execution

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
)

func TestPreparePhaseUpdateUsesOnlyStructuredPlan(t *testing.T) {
	update, ok := preparePhaseUpdate(codex.TurnPhaseEvent{
		TurnID: "turn-1", Phase: codex.TurnPhasePlanning,
		Step: "替换 /private/workspace 的桥接服务", Complete: 2, Total: 4,
	})
	if !ok || update.message != "Codex 阶段：计划 2/4 · 替换 [本机路径] 的桥接服务" {
		t.Fatalf("phase update = %#v, ok=%v", update, ok)
	}
	if strings.Contains(update.message, "/private") {
		t.Fatalf("phase leaked path: %q", update.message)
	}
}

func TestPreparePhaseUpdateRejectsInvalidFacts(t *testing.T) {
	for _, event := range []codex.TurnPhaseEvent{
		{Phase: codex.TurnPhaseStarted},
		{TurnID: "turn-1", Phase: codex.TurnPhase("invented")},
		{TurnID: "turn-1", Phase: codex.TurnPhasePlanning, Complete: 2, Total: 1},
		{TurnID: "turn-1", Phase: codex.TurnPhasePlanning, Complete: 0, Total: 0},
	} {
		if update, ok := preparePhaseUpdate(event); ok {
			t.Fatalf("invalid event accepted: %#v -> %#v", event, update)
		}
	}
}

func TestProgressConfigRejectsInvalidEnabledCadenceAtStartup(t *testing.T) {
	for _, config := range []ProgressConfig{
		{Enabled: true, FirstMessageDelay: time.Second},
		{Enabled: true, TypingInterval: time.Second, FirstMessageDelay: -time.Second},
	} {
		if err := config.Validate(); err == nil {
			t.Fatalf("invalid progress config was accepted: %#v", config)
		}
	}
	if err := (ProgressConfig{Enabled: false}).Validate(); err != nil {
		t.Fatalf("disabled progress should not require a delivery cadence: %v", err)
	}
}

func TestProgressReporterMergesDuplicatePhaseAndFreezesTerminal(t *testing.T) {
	var stages []string
	var reporter *ProgressReporter
	reporter, err := NewProgressReporter(context.Background(), ProgressConfig{Enabled: false}, ProgressCallbacks{
		Persist: func(stage string) {
			stages = append(stages, stage)
			// 模拟任务状态持久化期间重入一次迟到事件，验证 Report 不持锁调用外部函数。
			if len(stages) == 1 {
				_, _ = reporter.accept(codex.TurnPhaseEvent{TurnID: "turn-1", Phase: codex.TurnPhaseStarted})
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer reporter.Close()
	reporter.Report(codex.TurnPhaseEvent{TurnID: "turn-1", Phase: codex.TurnPhaseStarted})
	reporter.Report(codex.TurnPhaseEvent{TurnID: "turn-1", Phase: codex.TurnPhaseStarted})
	reporter.Report(codex.TurnPhaseEvent{TurnID: "turn-other", Phase: codex.TurnPhaseWorking})
	reporter.Report(codex.TurnPhaseEvent{TurnID: "turn-1", Phase: codex.TurnPhasePlanning, Step: "实现阶段状态机", Complete: 1, Total: 3})
	reporter.Report(codex.TurnPhaseEvent{TurnID: "turn-1", Phase: codex.TurnPhasePlanning, Step: "实现阶段状态机", Complete: 1, Total: 3})
	reporter.Report(codex.TurnPhaseEvent{TurnID: "turn-1", Phase: codex.TurnPhaseCompleted})
	reporter.Report(codex.TurnPhaseEvent{TurnID: "turn-1", Phase: codex.TurnPhaseWorking})

	want := []string{
		"Codex 阶段：轮次已开始",
		"Codex 阶段：计划 1/3 · 实现阶段状态机",
		"Codex 阶段：轮次已完成",
	}
	if strings.Join(stages, "|") != strings.Join(want, "|") {
		t.Fatalf("stages = %#v, want %#v", stages, want)
	}
}

func TestTruncateRunesIncludesEllipsisWithinLimit(t *testing.T) {
	got := presentation.Truncate(strings.Repeat("测", 200), 120)
	if len([]rune(got)) != 120 || !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated stage length=%d value=%q", len([]rune(got)), got)
	}
}

func TestProgressReporterSendsEachRealPhaseOnceAndStopsAtTerminal(t *testing.T) {
	var mu sync.Mutex
	var visible []string
	reporter, err := NewProgressReporter(context.Background(), ProgressConfig{
		Enabled: true, TypingInterval: time.Hour, FirstMessageDelay: 5 * time.Millisecond,
	}, ProgressCallbacks{
		SendTyping: func(context.Context) error { return nil },
		SendPhase: func(_ context.Context, message string) error {
			mu.Lock()
			visible = append(visible, message)
			mu.Unlock()
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	plan := codex.TurnPhaseEvent{TurnID: "turn-1", Phase: codex.TurnPhasePlanning, Step: "实现阶段状态机", Complete: 1, Total: 2}
	reporter.Report(codex.TurnPhaseEvent{TurnID: "turn-1", Phase: codex.TurnPhaseStarted})
	reporter.Report(codex.TurnPhaseEvent{TurnID: "turn-1", Phase: codex.TurnPhaseStarted})
	reporter.Report(plan)
	waitForVisiblePhaseCount(t, &mu, &visible, 1)
	reporter.Report(plan)
	reporter.Report(codex.TurnPhaseEvent{TurnID: "turn-1", Phase: codex.TurnPhaseWorking})
	waitForVisiblePhaseCount(t, &mu, &visible, 2)
	reporter.Report(codex.TurnPhaseEvent{TurnID: "turn-1", Phase: codex.TurnPhaseCompleted})
	reporter.Report(codex.TurnPhaseEvent{TurnID: "turn-1", Phase: codex.TurnPhaseFinalizing})
	reporter.Close()
	time.Sleep(20 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	want := []string{"Codex 阶段：计划 1/2 · 实现阶段状态机", "Codex 阶段：正在执行工作项"}
	if strings.Join(visible, "|") != strings.Join(want, "|") {
		t.Fatalf("visible phases = %#v, want %#v", visible, want)
	}
}

func waitForVisiblePhaseCount(t *testing.T, mu *sync.Mutex, visible *[]string, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		current := len(*visible)
		mu.Unlock()
		if current >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("visible phase count did not reach %d", count)
}
