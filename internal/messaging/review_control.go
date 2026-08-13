package messaging

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/session"
)

const reviewContinuePrompt = "根据当前线程中最近一次 Codex 代码审查结论，逐项核对并修复所有确实成立的问题。修改后运行与变更相关的测试或检查，最后总结已修复项、验证结果和仍需人工判断的风险。"

var (
	reviewPriorityPattern = regexp.MustCompile(`^\s*(?:[-*+]\s*)?\[(P[0-3])\]\s*(.+?)\s*$`)
	reviewLocationPattern = regexp.MustCompile(`(?i)([a-z0-9_.\\/-]+\.[a-z0-9]+:\d+(?::\d+)?)\)?\s*$`)
	reviewPrivatePath     = regexp.MustCompile(`(?i)(?:[a-z]:[\\/]|/)[^\s，。；;]+`)
)

type mobileReviewPackage struct {
	Verdict       string
	Headline      string
	Summary       string
	Workspace     string
	Thread        string
	Target        string
	Highest       string
	TotalFindings int
	Findings      []mobileReviewFinding
	Facts         []mobileReviewFact
}

type mobileReviewFinding struct {
	Priority string
	Title    string
	Detail   string
	Location string
}

func (h *Handler) reviewCurrentThread(ctx context.Context, userID string) string {
	return h.withRuntimeMutation(func() string {
		if h.projects == nil || h.sessions == nil {
			return "代码审查失败：Codex 工作空间或线程管理器不可用。"
		}
		threadClient, _, err := h.advancedThreadContext()
		if err != nil {
			return err.Error()
		}
		current, err := h.sessions.Current(ctx, userID, threadClient)
		if err != nil {
			return formatSessionError(err)
		}
		project := h.projects.Current(userID)
		return h.runMobileReview(ctx, userID, project.ID, current.Info.ID)
	})
}

func (h *Handler) reviewFrozenThread(ctx context.Context, userID, projectID, threadID string) string {
	return h.withRuntimeMutation(func() string {
		return h.runMobileReview(ctx, userID, projectID, threadID)
	})
}

func (h *Handler) runMobileReview(ctx context.Context, userID, projectID, threadID string) string {
	projectID = strings.TrimSpace(projectID)
	threadID = strings.TrimSpace(threadID)
	if h.projects == nil || h.sessions == nil || !h.sessions.OwnsProjectThread(userID, projectID, threadID) {
		return "代码审查失败：目标工作空间或线程已经失效。请发送 /status 重新选择目标线程。"
	}
	project, exists := h.projects.Get(projectID)
	if !exists {
		return "代码审查失败：目标工作空间已不在允许清单中。"
	}
	threadClient, advanced, err := h.advancedThreadContext()
	if err != nil {
		return err.Error()
	}
	thread, err := threadClient.ReadThread(ctx, threadID)
	if err != nil {
		return "代码审查失败：" + formatSessionError(err)
	}
	evidence := mobileReviewEvidence{}
	if factClient, ok := threadClient.(codex.ThreadFactClient); ok {
		if facts, factErr := factClient.ReadThreadVerificationFacts(ctx, threadID); factErr == nil {
			evidence.Verification = facts
		}
	}
	review, err := h.sessions.ReviewProjectThread(
		ctx, userID, projectID, threadID, advanced,
		codex.ReviewTarget{Type: "uncommittedChanges"}, nil,
	)
	if err != nil {
		return "代码审查失败：" + err.Error()
	}
	review = strings.TrimSpace(review)
	if review == "" {
		return "代码审查失败：Codex 没有返回审查结论。"
	}
	// 原生 review/start 结论保留 30 分钟供显式取回，不进入持久菜单和日志。
	h.visualReplies.Store(userID, &cachedVisualReply{
		Text:      "# Codex 代码审查原文\n\n" + limitReviewText(review, visualReplyMaxRunes),
		ExpiresAt: time.Now().Add(visualReplyCacheTTL),
	})
	evidence.Changes = inspectWorkspaceChangeFacts(ctx, project.Root)
	if h.deliveries != nil {
		evidence.Deliveries = h.deliveries.SummaryForThread(userID, projectID, threadID)
	}
	packet := buildMobileReviewPackage(review, project.Name, threadTitle(thread), evidence)
	options := []controlOption{
		{Label: "继续修复 · 当前线程", Action: actionReviewContinue, Value: threadID, Query: projectID},
		{Label: "接受结论 · 结束审查", Action: actionReviewAccept, Value: threadID, Query: projectID},
		{Label: "重新审查 · /review", Action: actionReviewRerun, Value: threadID, Query: projectID},
	}
	if !h.storeChoice(userID, viewSessionReview, options, actionCodexDevelopment) {
		return controlStateFailureResult().Text
	}
	return formatMobileReviewPackage(packet, options)
}

func (h *Handler) continueReviewTarget(userID, projectID, threadID string) ActionResult {
	if !h.validFrozenReviewTarget(userID, projectID, threadID) {
		return controlTextResult(actionReviewContinue, DomainSession, "审查目标已经失效。发送 /status 重新选择目标线程。")
	}
	return effectActionResult(
		string(actionReviewContinue), DomainSession, "", EffectEnqueuePrompt, reviewContinuePrompt,
	).withProjectID(projectID).withThreadID(threadID)
}

func (h *Handler) acceptReviewTarget(userID, projectID, threadID string) string {
	if !h.validFrozenReviewTarget(userID, projectID, threadID) {
		return "审查目标已经失效。发送 /status 重新选择目标线程。"
	}
	project, _ := h.projects.Get(projectID)
	return strings.Join([]string{
		"审查结论已接受",
		"工作空间：" + project.Name,
		"目标线程：" + session.ShortCode(threadID),
		"本次确认只结束移动审查流程，不会提交、推送或部署代码。",
	}, "\n")
}

func (h *Handler) validFrozenReviewTarget(userID, projectID, threadID string) bool {
	if h.projects == nil || h.sessions == nil {
		return false
	}
	if _, exists := h.projects.Get(strings.TrimSpace(projectID)); !exists {
		return false
	}
	return h.sessions.OwnsProjectThread(userID, projectID, threadID)
}

func buildMobileReviewPackage(raw, workspace, thread string, evidence mobileReviewEvidence) mobileReviewPackage {
	findings := parseMobileReviewFindings(raw)
	total := len(findings)
	highest := highestReviewPriority(findings)
	if len(findings) > 3 {
		findings = findings[:3]
	}
	packet := mobileReviewPackage{
		Workspace: strings.TrimSpace(workspace), Thread: strings.TrimSpace(thread),
		Target: "未提交改动", Highest: highest, TotalFindings: total, Findings: findings,
		Facts: mobileReviewFacts(evidence),
	}
	switch {
	case total > 0:
		packet.Verdict = "attention"
		packet.Headline = fmt.Sprintf("发现 %d 项需要判断", total)
		packet.Summary = "优先处理高等级问题；卡片仅展示前三项，完整证据可取回原文。"
	case reviewLooksClear(raw):
		packet.Verdict = "clear"
		packet.Headline = "本轮未发现明确问题"
		packet.Summary = "这是代码审查结论，不替代项目测试、产品验收或人工风险判断。"
	default:
		packet.Verdict = "advisory"
		packet.Headline = "审查完成，结论需阅读"
		packet.Summary = "审查输出没有标准优先级条目，避免自动推断为通过。"
	}
	return packet
}

func parseMobileReviewFindings(raw string) []mobileReviewFinding {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	var findings []mobileReviewFinding
	var current *mobileReviewFinding
	flush := func() {
		if current == nil {
			return
		}
		current.Title, current.Location = splitReviewTitleLocation(current.Title)
		current.Title = sanitizeReviewDisplayText(current.Title, 54)
		current.Detail = sanitizeReviewDisplayText(current.Detail, 84)
		current.Location = sanitizeReviewLocation(current.Location)
		if current.Title != "" {
			findings = append(findings, *current)
		}
		current = nil
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if match := reviewPriorityPattern.FindStringSubmatch(trimmed); len(match) == 3 {
			flush()
			current = &mobileReviewFinding{Priority: match[1], Title: match[2]}
			continue
		}
		if current == nil || trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		trimmed = strings.TrimSpace(strings.TrimLeft(trimmed, "-*+ "))
		if trimmed == "" {
			continue
		}
		if current.Detail == "" {
			current.Detail = trimmed
		} else if utf8.RuneCountInString(current.Detail) < 180 {
			current.Detail += " " + trimmed
		}
	}
	flush()
	return findings
}

func splitReviewTitleLocation(title string) (string, string) {
	title = strings.TrimSpace(title)
	for _, separator := range []string{" — ", " – "} {
		if index := strings.LastIndex(title, separator); index > 0 {
			candidate := strings.TrimSpace(title[index+len(separator):])
			if reviewLocationPattern.MatchString(candidate) {
				return strings.TrimSpace(title[:index]), candidate
			}
		}
	}
	if match := reviewLocationPattern.FindStringIndex(title); match != nil && match[0] > 0 {
		return strings.TrimSpace(strings.TrimRight(title[:match[0]], "—–- (")), strings.TrimSpace(title[match[0]:match[1]])
	}
	return title, ""
}

func sanitizeReviewLocation(location string) string {
	location = strings.Trim(strings.TrimSpace(location), "`()[]")
	if location == "" {
		return ""
	}
	location = strings.ReplaceAll(location, "\\", "/")
	parts := strings.Split(location, "/")
	if len(parts) > 2 {
		parts = parts[len(parts)-2:]
	}
	return normalizeReviewRunes(strings.Join(parts, "/"), 72)
}

func sanitizeReviewDisplayText(value string, limit int) string {
	value = strings.TrimSpace(strings.NewReplacer("`", "", "**", "", "__", "").Replace(value))
	value = reviewPrivatePath.ReplaceAllStringFunc(value, sanitizeReviewLocation)
	value = strings.Join(strings.Fields(value), " ")
	return normalizeReviewRunes(value, limit)
}

func normalizeReviewRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

func highestReviewPriority(findings []mobileReviewFinding) string {
	if len(findings) == 0 {
		return ""
	}
	priorities := make([]string, 0, len(findings))
	for _, finding := range findings {
		priorities = append(priorities, finding.Priority)
	}
	sort.Strings(priorities)
	return priorities[0]
}

func reviewLooksClear(raw string) bool {
	normalized := strings.ToLower(strings.Join(strings.Fields(raw), " "))
	for _, phrase := range []string{
		"no findings", "no issues found", "looks solid overall",
		"没有发现问题", "未发现问题", "没有发现阻断问题", "未发现阻断问题", "未发现需要修复",
	} {
		if strings.Contains(normalized, phrase) {
			return true
		}
	}
	return false
}

func formatMobileReviewPackage(packet mobileReviewPackage, options []controlOption) string {
	lines := []string{
		"Codex 移动审查",
		"结论：" + packet.Headline,
		"工作空间：" + normalizeReviewRunes(packet.Workspace, 36),
		"目标线程：" + normalizeReviewRunes(packet.Thread, 48),
		"审查范围：" + packet.Target,
		"审查状态：" + packet.Verdict,
	}
	if packet.Highest != "" {
		lines = append(lines, "最高优先级："+packet.Highest)
	}
	lines = append(lines, "摘要："+packet.Summary)
	if len(packet.Facts) > 0 {
		lines = append(lines, "", "可验证事实")
		for _, fact := range packet.Facts {
			lines = append(lines, fact.Label+"："+fact.Value)
		}
	}
	if len(packet.Findings) > 0 {
		lines = append(lines, "", "重点问题")
		for _, finding := range packet.Findings {
			lines = append(lines, "["+finding.Priority+"] "+finding.Title)
			if finding.Location != "" {
				lines = append(lines, "位置："+finding.Location)
			}
			if finding.Detail != "" {
				lines = append(lines, finding.Detail)
			}
		}
	}
	lines = append(lines, "", renderControlOptions(options), "", "回复数字继续；回复“文字版”获取完整审查原文；0 返回 Codex 开发。")
	return strings.Join(lines, "\n")
}

func limitReviewText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	return normalizeReviewRunes(value, limit) + "\n\n[原文超过移动端取回上限，已截断]"
}
