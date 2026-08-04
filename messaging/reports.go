package messaging

import (
	"fmt"
	"strings"
)

// ScheduledReportStatus 是微信只读管理界面需要的确定性巡检摘要。
type ScheduledReportStatus struct {
	Name       string
	State      string
	Schedule   string
	Timezone   string
	NextRun    string
	LastSent   string
	ProjectDir string
	Service    string
	HealthURL  string
}

type ScheduledReportProvider interface {
	ScheduledReportStatuses(userID string) []ScheduledReportStatus
}

func (h *Handler) scheduledReportStatuses(userID string) []ScheduledReportStatus {
	if h.reports == nil {
		return nil
	}
	return h.reports.ScheduledReportStatuses(userID)
}

func (h *Handler) openScheduledReports(userID string, pageNumber int) string {
	statuses := h.scheduledReportStatuses(userID)
	if len(statuses) == 0 {
		return "定时巡检\n\n当前没有配置巡检计划。"
	}
	if pageNumber <= 0 {
		pageNumber = 1
	}
	totalPages := (len(statuses) + controlSessionPageSize - 1) / controlSessionPageSize
	if pageNumber > totalPages {
		return fmt.Sprintf("巡检页面不存在：%d / %d。发送 / 重新打开菜单。", pageNumber, totalPages)
	}
	start := (pageNumber - 1) * controlSessionPageSize
	end := start + controlSessionPageSize
	if end > len(statuses) {
		end = len(statuses)
	}
	options := make([]controlOption, 0, controlSessionPageSize+2)
	for _, status := range statuses[start:end] {
		options = append(options, controlOption{
			Label:  fmt.Sprintf("%s · %s", status.Name, status.State),
			Action: actionScheduledReport, Value: status.Name, Page: pageNumber,
		})
	}
	if pageNumber > 1 {
		options = append(options, controlOption{
			Label:  fmt.Sprintf("上一页 · %d/%d", pageNumber-1, totalPages),
			Action: actionScheduledReports, Page: pageNumber - 1,
		})
	}
	if pageNumber < totalPages {
		options = append(options, controlOption{
			Label:  fmt.Sprintf("下一页 · %d/%d", pageNumber+1, totalPages),
			Action: actionScheduledReports, Page: pageNumber + 1,
		})
	}
	prompt := strings.Join([]string{
		"定时巡检",
		"",
		fmt.Sprintf("页码：%d / %d", pageNumber, totalPages),
		fmt.Sprintf("计划：%d", len(statuses)),
		"最近下次：" + nearestReportRun(statuses),
		"",
		renderControlOptions(options),
	}, "\n")
	h.storeChoice(userID, prompt, options, actionMain)
	return prompt + "\n\n回复数字查看详情，或说“下一页”“上一页”；0 返回。"
}

func (h *Handler) openScheduledReport(userID, name string, pageNumber int) string {
	if pageNumber <= 0 {
		pageNumber = 1
	}
	var selected *ScheduledReportStatus
	for _, status := range h.scheduledReportStatuses(userID) {
		if status.Name == name {
			copy := status
			selected = &copy
			break
		}
	}
	if selected == nil {
		return "巡检计划已经变化。发送“定时巡检”刷新列表。"
	}
	options := []controlOption{
		{Label: "刷新巡检详情", Action: actionScheduledReport, Value: selected.Name, Page: pageNumber},
		{Label: "返回巡检列表", Action: actionScheduledReports, Page: pageNumber},
	}
	lines := []string{
		"巡检详情：" + selected.Name,
		"",
		"状态：" + selected.State,
		"计划：" + selected.Schedule,
		"时区：" + selected.Timezone,
		"下次：" + selected.NextRun,
		"上次：" + emptyReportValue(selected.LastSent),
		"项目：" + selected.ProjectDir,
		"服务：" + selected.Service,
		"健康端点：" + selected.HealthURL,
		"",
		renderControlOptions(options),
	}
	prompt := strings.Join(lines, "\n")
	h.storeChoiceWithBack(userID, prompt, options, controlOption{Action: actionScheduledReports, Page: pageNumber})
	return prompt + "\n\n回复数字操作，0 返回巡检列表。"
}

func nearestReportRun(statuses []ScheduledReportStatus) string {
	nearest := "未计算"
	for _, status := range statuses {
		if status.NextRun != "" && (nearest == "未计算" || status.NextRun < nearest) {
			nearest = status.NextRun
		}
	}
	return nearest
}

func emptyReportValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "尚未发送"
	}
	return value
}
