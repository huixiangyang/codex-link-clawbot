package bridge

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/thread"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
)

func TestBuildMobileReviewPackageExtractsPriorityAndRedactsPrivatePath(t *testing.T) {
	raw := strings.Join([]string{
		"## Findings",
		"- [P1] Do not lose the delivery receipt — /root/CODES/codex-link-clawbot/internal/bridge/handler.go:221",
		"  Returning early leaves /root/.codex-link-clawbot/private/state.json stale and retries the visible result.",
		"- [P2] Preserve the target thread — internal/thread/manager.go:407",
		"  The follow-up must not use the newly selected thread.",
	}, "\n")
	packet := buildMobileReviewPackage(raw, "codex-link-clawbot", "移动审查", mobileReviewEvidence{})
	if packet.Verdict != "attention" || packet.Highest != "P1" || packet.TotalFindings != 2 || len(packet.Findings) != 2 {
		t.Fatalf("packet = %#v", packet)
	}
	if packet.Findings[0].Location != "bridge/handler.go:221" || strings.Contains(packet.Findings[0].Detail, "/root/") {
		t.Fatalf("private path was not reduced: %#v", packet.Findings[0])
	}
	if packet.Findings[1].Location != "thread/manager.go:407" {
		t.Fatalf("relative location = %q", packet.Findings[1].Location)
	}
}

func TestBuildMobileReviewPackageDoesNotInventClearVerdict(t *testing.T) {
	advisory := buildMobileReviewPackage("Review completed. Please inspect the notes below.", "codex-link-clawbot", "审查线程", mobileReviewEvidence{})
	if advisory.Verdict != "advisory" || !strings.Contains(advisory.Headline, "需阅读") {
		t.Fatalf("advisory packet = %#v", advisory)
	}
	clear := buildMobileReviewPackage("No findings.", "codex-link-clawbot", "审查线程", mobileReviewEvidence{})
	if clear.Verdict != "clear" || clear.Highest != "" {
		t.Fatalf("clear packet = %#v", clear)
	}
}

func TestMobileReviewActionsFreezeProjectAndThread(t *testing.T) {
	handler, runtime := newSessionHandler(t)
	_ = controlReply(t, handler, "owner-1", "新建线程 移动审查")
	threadID := handler.sessions.SnapshotThreadID("owner-1", "workspace")
	runtime.reviewText = "- [P1] 修复竞态 — /root/CODES/codex-link-clawbot/internal/bridge/control.go:88\n  同一菜单不能执行两次。"
	runtime.verification = codex.ThreadVerificationFacts{Available: true, Total: 2, Passed: 2, Kinds: []codex.VerificationKind{codex.VerificationTest}}

	review := controlReply(t, handler, "owner-1", "代码审查")
	for _, want := range []string{"Codex 移动审查", "发现 1 项需要判断", "验证事实：2 项 · 2 通过 · 测试", "[P1] 修复竞态", "位置：bridge/control.go:88"} {
		if !strings.Contains(review, want) {
			t.Fatalf("review missing %q: %q", want, review)
		}
	}
	stateData, err := os.ReadFile(handler.controlStates.path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stateData), "修复竞态") || strings.Contains(string(stateData), "/root/CODES") || strings.Contains(string(stateData), reviewContinuePrompt) {
		t.Fatalf("review content leaked into persistent menu: %s", stateData)
	}
	cached, exists := handler.visualReplies.Load("owner-1")
	if !exists || !strings.Contains(cached.(*cachedVisualReply).Text, "/root/CODES/codex-link-clawbot") {
		t.Fatalf("review source was not retained in the private short-lived cache: %#v", cached)
	}
	result, handled := handler.handleControlInput(context.Background(), "owner-1", "1", false, nextTestControlSource())
	if !handled || result.Effect.Kind != EffectEnqueuePrompt || result.Effect.ProjectID != "workspace" ||
		result.Effect.ThreadID != threadID || result.Effect.Value != reviewContinuePrompt {
		t.Fatalf("continue result = %#v handled=%v", result, handled)
	}

	_ = controlReply(t, handler, "owner-1", "代码审查")
	accepted := controlReply(t, handler, "owner-1", "2")
	if !strings.Contains(accepted, "审查结论已接受") || !strings.Contains(accepted, thread.ShortCode(threadID)) || strings.Contains(accepted, "已提交") {
		t.Fatalf("accepted = %q", accepted)
	}

	before := len(runtime.reviewCalls)
	_ = controlReply(t, handler, "owner-1", "代码审查")
	rerun := controlReply(t, handler, "owner-1", "3")
	if !strings.Contains(rerun, "Codex 移动审查") || len(runtime.reviewCalls) != before+2 || runtime.reviewCalls[len(runtime.reviewCalls)-1] != threadID {
		t.Fatalf("rerun=%q calls=%#v", rerun, runtime.reviewCalls)
	}
}

func TestMobileReviewBuildsDedicatedStructuredVisual(t *testing.T) {
	packet := mobileReviewPackage{
		Verdict: "attention", Headline: "发现 1 项需要判断", Workspace: "codex-link-clawbot", Thread: "移动审查",
		Target: "未提交改动", Highest: "P1", Summary: "优先处理高等级问题。",
		Facts:    []mobileReviewFact{{Label: "变更事实", Value: "12 个文件"}, {Label: "验证事实", Value: "3 项通过"}, {Label: "交付事实", Value: "1 项可再次发送"}},
		Findings: []mobileReviewFinding{{Priority: "P1", Title: "修复投递竞态", Location: "bridge/handler.go:221", Detail: "避免重复发送。"}},
	}
	options := []controlOption{{Label: "继续修复 · 当前线程"}, {Label: "接受结论 · 结束审查"}, {Label: "重新审查 · /review"}}
	review := mobileReviewVisual(packet, options)
	if review.Verdict != visual.ReviewVerdictAttention || review.Highest != "P1" || len(review.Facts) != 3 || len(review.Findings) != 1 || len(review.Options) != 3 {
		t.Fatalf("review = %#v", review)
	}
	if review.Findings[0].Location != "bridge/handler.go:221" || review.Options[0].Number != "1" {
		t.Fatalf("review details = %#v", review)
	}
}
