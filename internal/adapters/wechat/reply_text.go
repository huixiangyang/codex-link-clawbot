package wechat

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/adapters/wechat/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/request"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/target"
	"github.com/huixiangyang/codex-link-clawbot/internal/core/thread"
)

func taskTargetLabel(task request.Task) string {
	if task.ThreadID != "" {
		return thread.ShortCode(task.ThreadID)
	}
	return "当前工作空间的新会话"
}

func (h *Handler) resultHeading(task request.Task) string {
	project := task.ProjectID
	if h.projects != nil {
		if definition, ok := h.projects.Get(project); ok {
			project = definition.Name
		}
	}
	name := task.Summary
	if h.sessionClient != nil && task.ThreadID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if info, err := h.sessionClient.ReadThread(ctx, task.ThreadID); err == nil && info.Name != "" {
			name = info.Name
		}
	}
	return fmt.Sprintf("结果 %s · %s\n会话：%s · %s", shortTaskID(task.ID), project, presentation.NormalizeLine(name, 36), taskTargetLabel(task))
}

func isCurrentTask(targets *target.Store, task request.Task) bool {
	if targets == nil {
		return false
	}
	current := targets.Current(task.OwnerID)
	return current.WorkspaceID == task.ProjectID && (current.ID == task.TargetID || task.ThreadID != "" && current.ThreadID == task.ThreadID)
}

func (h *Handler) resultContext(task request.Task, body string) string {
	if isCurrentTask(h.targets, task) {
		return body
	}
	return h.resultHeading(task) + "\n\n" + body
}

func resultShortcuts(task request.Task, result request.Result, brief bool) string {
	code := shortTaskID(task.ID)
	text := ""
	if brief {
		text = "\n\n回答已节选，发送“全文 " + code + "”查看完整内容。"
	}
	if len(result.Artifacts)+len(result.ImageURLs) > 0 {
		text += "\n\n发送“文件 " + code + "”取回文件。"
	}
	return text
}

// 全文分段发送；任何已成功片段都计入可见性，失败后不能谎报完全未发送。
func sendResultText(ctx context.Context, client *ilink.Client, owner, body, token string) deliveryReport {
	report := deliveryReport{Outcome: request.DeliverySucceeded}
	runes := []rune(body)
	const pageRunes = 1100
	pages := max(1, (len(runes)+pageRunes-1)/pageRunes)
	heading := presentation.NormalizeLine(strings.SplitN(body, "\n", 2)[0], 80)
	for page := 0; page < pages; page++ {
		part := string(runes[page*pageRunes : min(len(runes), (page+1)*pageRunes)])
		if pages > 1 {
			part = fmt.Sprintf("%s · %d/%d\n", heading, page+1, pages) + part
		}
		if err := sendPlainTextReply(ctx, client, owner, part, token, NewClientID()); err != nil {
			report.Outcome, report.Failure = request.DeliveryExplicitFailure, request.ReasonDeliveryFailed
			if report.TextSent || outboundMayBeVisible(err) {
				report.Outcome, report.Failure = request.DeliveryAmbiguous, request.ReasonDeliveryAmbiguous
			}
			return report
		}
		report.TextSent = true
	}
	return report
}
