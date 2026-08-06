package messaging

import (
	"context"
	"fmt"
	"strings"
)

type AutomationStatus struct {
	ID          string
	Name        string
	State       string
	Schedule    string
	Timezone    string
	NextRun     string
	LastRun     string
	LastSent    string
	ProjectID   string
	ProjectName string
	Checks      []string
	NotifyOn    string
}

type AutomationProvider interface {
	AutomationStatuses(userID string) []AutomationStatus
	RunAutomation(ctx context.Context, userID, automationID string) (string, error)
}

func (h *Handler) automationStatuses(userID string) []AutomationStatus {
	if h.automations == nil {
		return nil
	}
	return h.automations.AutomationStatuses(userID)
}

func (h *Handler) openAutomations(userID string, pageNumber int) string {
	statuses := h.automationStatuses(userID)
	if len(statuses) == 0 {
		return "自动化中心\n\n当前没有配置自动检查。"
	}
	if pageNumber <= 0 {
		pageNumber = 1
	}
	totalPages := (len(statuses) + controlSessionPageSize - 1) / controlSessionPageSize
	if pageNumber > totalPages {
		return fmt.Sprintf("自动化页面不存在：%d / %d。发送 / 重新打开菜单。", pageNumber, totalPages)
	}
	start := (pageNumber - 1) * controlSessionPageSize
	end := start + controlSessionPageSize
	if end > len(statuses) {
		end = len(statuses)
	}
	options := make([]controlOption, 0, controlSessionPageSize+2)
	for _, status := range statuses[start:end] {
		options = append(options, controlOption{
			Label:  status.Name + " · " + status.State,
			Action: actionAutomation, Value: status.ID, Page: pageNumber,
		})
	}
	if pageNumber > 1 {
		options = append(options, controlOption{Label: fmt.Sprintf("上一页 · %d/%d", pageNumber-1, totalPages), Action: actionAutomations, Page: pageNumber - 1})
	}
	if pageNumber < totalPages {
		options = append(options, controlOption{Label: fmt.Sprintf("下一页 · %d/%d", pageNumber+1, totalPages), Action: actionAutomations, Page: pageNumber + 1})
	}
	prompt := strings.Join([]string{
		"自动化中心", "",
		fmt.Sprintf("页码：%d / %d", pageNumber, totalPages),
		fmt.Sprintf("计划：%d", len(statuses)),
		"最近下次：" + nearestAutomationRun(statuses), "", renderControlOptions(options),
	}, "\n")
	if !h.storeChoice(userID, viewAutomationCenter, options, actionMain) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字查看详情，或说“下一页”“上一页”；0 返回。"
}

func (h *Handler) openAutomation(userID, id string, pageNumber int) string {
	if pageNumber <= 0 {
		pageNumber = 1
	}
	var selected *AutomationStatus
	for _, status := range h.automationStatuses(userID) {
		if status.ID == id {
			copy := status
			selected = &copy
			break
		}
	}
	if selected == nil {
		return "自动化计划已经变化。发送“自动化”刷新列表。"
	}
	options := []controlOption{
		{Label: "立即检查", Action: actionRunAutomation, Value: selected.ID, Page: pageNumber},
		{Label: "刷新详情", Action: actionAutomation, Value: selected.ID, Page: pageNumber},
		{Label: "返回自动化中心", Action: actionAutomations, Page: pageNumber},
	}
	lines := []string{
		"自动化详情：" + selected.Name, "",
		"状态：" + selected.State,
		"项目：" + selected.ProjectName,
		"计划：" + selected.Schedule,
		"时区：" + selected.Timezone,
		"检查：" + strings.Join(selected.Checks, "、"),
		"通知：" + formatNotifyPolicy(selected.NotifyOn),
		"下次：" + emptyAutomationValue(selected.NextRun, "尚未计算"),
		"上次运行：" + emptyAutomationValue(selected.LastRun, "尚未运行"),
		"上次通知：" + emptyAutomationValue(selected.LastSent, "尚未通知"),
		"", renderControlOptions(options),
	}
	prompt := strings.Join(lines, "\n")
	if !h.storeChoiceWithBack(userID, viewAutomationDetail, options, controlOption{Action: actionAutomations, Page: pageNumber}) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字操作，0 返回自动化中心。"
}

func (h *Handler) runAutomation(ctx context.Context, userID, id string, pageNumber int) string {
	if h.automations == nil {
		return "自动化中心当前不可用。"
	}
	result, err := h.automations.RunAutomation(ctx, userID, id)
	if err != nil {
		return fmt.Sprintf("手动检查失败：%v", err)
	}
	options := []controlOption{
		{Label: "返回自动化详情", Action: actionAutomation, Value: id, Page: pageNumber},
		{Label: "返回自动化中心", Action: actionAutomations, Page: pageNumber},
	}
	prompt := result + "\n\n" + renderControlOptions(options)
	if !h.storeChoiceWithBack(userID, viewAutomationResult, options, controlOption{Action: actionAutomation, Value: id, Page: pageNumber}) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字继续，0 返回。"
}

func nearestAutomationRun(statuses []AutomationStatus) string {
	nearest := "未计算"
	for _, status := range statuses {
		if status.NextRun != "" && (nearest == "未计算" || status.NextRun < nearest) {
			nearest = status.NextRun
		}
	}
	return nearest
}

func formatNotifyPolicy(policy string) string {
	switch policy {
	case "always":
		return "每次运行"
	case "anomaly":
		return "仅异常"
	case "change":
		return "仅变化"
	case "anomaly_or_change":
		return "异常或变化"
	default:
		return policy
	}
}

func emptyAutomationValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
