package messaging

import (
	"fmt"
	"strings"
	"time"

	"github.com/huixiangyang/weclaw/session"
)

func (h *Handler) openActivities(userID string, pageNumber int) string {
	if h.activities == nil {
		return "任务记录\n\n任务记录当前不可用。"
	}
	records := h.activities.List(userID)
	if len(records) == 0 {
		return "任务记录\n\n还没有 Codex 任务记录。直接发送内容即可开始。"
	}
	if pageNumber <= 0 {
		pageNumber = 1
	}
	totalPages := (len(records) + controlSessionPageSize - 1) / controlSessionPageSize
	if pageNumber > totalPages {
		return fmt.Sprintf("任务记录页面不存在：%d / %d。发送 / 重新打开菜单。", pageNumber, totalPages)
	}
	start := (pageNumber - 1) * controlSessionPageSize
	end := start + controlSessionPageSize
	if end > len(records) {
		end = len(records)
	}
	options := make([]controlOption, 0, controlSessionPageSize+2)
	for _, record := range records[start:end] {
		options = append(options, controlOption{
			Label:  normalizeSessionLine(record.Summary, 38) + " · " + formatActivityStatus(record.Status),
			Action: actionActivityDetail, Value: record.ID, Page: pageNumber,
		})
	}
	if pageNumber > 1 {
		options = append(options, controlOption{
			Label:  fmt.Sprintf("上一页 · %d/%d", pageNumber-1, totalPages),
			Action: actionActivityPage, Page: pageNumber - 1,
		})
	}
	if pageNumber < totalPages {
		options = append(options, controlOption{
			Label:  fmt.Sprintf("下一页 · %d/%d", pageNumber+1, totalPages),
			Action: actionActivityPage, Page: pageNumber + 1,
		})
	}
	completed, exceptional := activityCounts(records)
	prompt := strings.Join([]string{
		"任务记录",
		"",
		fmt.Sprintf("页码：%d / %d", pageNumber, totalPages),
		fmt.Sprintf("记录：%d", len(records)),
		fmt.Sprintf("完成：%d", completed),
		fmt.Sprintf("异常：%d", exceptional),
		"",
		renderControlOptions(options),
	}, "\n")
	h.storeChoice(userID, prompt, options, actionMain)
	return prompt + "\n\n回复数字查看详情，或说“下一页”“上一页”；0 返回。"
}

func (h *Handler) openActivityDetail(userID, id string, pageNumber int) string {
	if h.activities == nil {
		return "任务详情\n\n任务记录当前不可用。"
	}
	record, ok := h.activities.Find(userID, id)
	if !ok {
		return "任务记录已经变化。发送“任务记录”刷新列表。"
	}
	if pageNumber <= 0 {
		pageNumber = 1
	}
	finishedAt := "尚未结束"
	end := time.Now()
	if record.FinishedAt > 0 {
		end = time.Unix(record.FinishedAt, 0)
		finishedAt = formatSessionTime(record.FinishedAt)
	}
	duration := end.Sub(time.Unix(record.StartedAt, 0))
	if duration < 0 {
		duration = 0
	}
	options := []controlOption{
		{Label: "返回任务记录", Action: actionActivityPage, Page: pageNumber},
		{Label: "当前任务状态", Action: actionTaskStatus},
	}
	prompt := strings.Join([]string{
		"任务详情",
		"",
		"摘要：" + record.Summary,
		"状态：" + formatActivityStatus(record.Status),
		"开始：" + formatSessionTime(record.StartedAt),
		"结束：" + finishedAt,
		"用时：" + formatUptime(duration),
	}, "\n")
	if record.ProjectID != "" {
		prompt += "\n项目：" + record.ProjectID
	}
	if record.SessionID != "" {
		prompt += "\n会话：" + session.ShortCode(record.SessionID)
	}
	if record.TotalTokens > 0 {
		prompt += fmt.Sprintf("\n用量：输入 %d · 输出 %d · 合计 %d tokens", record.InputTokens, record.OutputTokens, record.TotalTokens)
	}
	prompt += "\n\n" + strings.Join([]string{
		renderControlOptions(options),
	}, "\n")
	h.storeChoiceWithBack(userID, prompt, options, controlOption{Action: actionActivityPage, Page: pageNumber})
	return prompt + "\n\n回复数字继续，0 返回原列表。"
}

func formatActivityStatus(status ActivityStatus) string {
	switch status {
	case ActivityRunning:
		return "运行中"
	case ActivitySucceeded:
		return "已完成"
	case ActivityFailed:
		return "失败"
	case ActivityCancelled:
		return "已取消"
	case ActivityInterrupted:
		return "重启中断"
	default:
		return "未知"
	}
}

func activityCounts(records []ActivityRecord) (completed, exceptional int) {
	for _, record := range records {
		switch record.Status {
		case ActivitySucceeded:
			completed++
		case ActivityFailed, ActivityCancelled, ActivityInterrupted:
			exceptional++
		}
	}
	return completed, exceptional
}
