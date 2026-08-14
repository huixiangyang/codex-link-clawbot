package bridge

import (
	"fmt"
	"github.com/huixiangyang/codex-link-clawbot/internal/control"
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/delivery"
	"github.com/huixiangyang/codex-link-clawbot/internal/thread"
)

func (h *Handler) openDeliveryBox(userID string) string {
	if h.deliveries == nil {
		return "交付箱当前不可用。"
	}
	return h.openDeliveryPage(userID, 1)
}

func (h *Handler) openDeliveryPage(userID string, pageNumber int) string {
	if h.deliveries == nil {
		return "交付箱当前不可用。"
	}
	records := h.deliveries.List(userID)
	if len(records) == 0 {
		return "交付箱\n\n当前没有交付记录。Codex 产生并成功发送的文件会在这里保留私有副本。"
	}
	if pageNumber <= 0 {
		pageNumber = 1
	}
	totalPages := (len(records) + controlSessionPageSize - 1) / controlSessionPageSize
	if pageNumber > totalPages {
		return fmt.Sprintf("交付页面不存在：%d / %d。", pageNumber, totalPages)
	}
	start := (pageNumber - 1) * controlSessionPageSize
	end := start + controlSessionPageSize
	if end > len(records) {
		end = len(records)
	}
	options := make([]controlOption, 0, controlSessionPageSize+2)
	for _, record := range records[start:end] {
		label := normalizeSessionLine(record.Title, 38)
		if h.deliveries.Availability(record) != delivery.Available {
			label += " · 已失效"
		}
		options = append(options, controlOption{
			Label: label, Action: actionDeliveryDetail,
			Value: record.ID, Page: pageNumber,
		})
	}
	if pageNumber > 1 {
		options = append(options, controlOption{Label: fmt.Sprintf("上一页 · %d/%d", pageNumber-1, totalPages), Action: actionDeliveryPage, Page: pageNumber - 1})
	}
	if pageNumber < totalPages {
		options = append(options, controlOption{Label: fmt.Sprintf("下一页 · %d/%d", pageNumber+1, totalPages), Action: actionDeliveryPage, Page: pageNumber + 1})
	}
	prompt := strings.Join([]string{
		"交付箱", "",
		fmt.Sprintf("页码：%d / %d", pageNumber, totalPages),
		fmt.Sprintf("交付物：%d", len(records)),
		"每个文件都来自已经成功发送的 Codex 结果，并保留私有副本。",
		"", renderControlOptions(options),
	}, "\n")
	if !h.storeChoiceWithBack(userID, viewDeliveryPage, options, controlOption{Action: actionMain}) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字查看详情，0 返回操作总览。"
}

func (h *Handler) openDeliveryDetail(userID, id string, pageNumber int) string {
	if h.deliveries == nil {
		return "交付箱当前不可用。"
	}
	record, availability, exists := h.deliveries.Verify(userID, id)
	if !exists {
		return "这条交付记录已经变化，请刷新列表。"
	}
	options := make([]controlOption, 0, 2)
	if availability == delivery.Available {
		options = append(options, controlOption{Label: "再次发送", Action: actionResendDelivery, Value: record.ID, Page: pageNumber})
	}
	options = append(options, controlOption{Label: "返回交付箱", Action: actionDeliveryPage, Page: pageNumber})
	status := "可再次发送 · SHA-256 已校验"
	if availability != delivery.Available {
		status = "已失效 · 私有副本缺失或校验失败"
	}
	prompt := strings.Join([]string{
		"交付详情", "",
		"名称：" + record.Title,
		"状态：" + status,
		"Codex 工作空间：" + record.ProjectID,
		"Codex 线程：" + thread.ShortCode(record.ThreadID),
		"codex-link-clawbot 请求：" + shortTaskID(record.TaskID),
		"时间：" + time.Unix(record.CreatedAt, 0).Local().Format("2006-01-02 15:04"),
		"大小：" + formatBytes(record.Size),
	}, "\n")
	if availability != delivery.Available {
		prompt += "\n\n该副本不可恢复，codex-link-clawbot 不会静默重跑原请求。需要重新生成时，请回到来源线程明确发起新请求。"
	}
	prompt += "\n\n" + renderControlOptions(options)
	if !h.storeChoiceWithBack(userID, viewDeliveryDetail, options, controlOption{Action: actionDeliveryPage, Page: pageNumber}) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字操作，0 返回交付箱。"
}

func (h *Handler) resendDelivery(userID, id string, pageNumber int) ActionResult {
	if h.deliveries == nil {
		return newActionResult(string(actionResendDelivery), control.DomainDelivery, "交付箱当前不可用。")
	}
	record, availability, exists := h.deliveries.Verify(userID, id)
	if !exists {
		return newActionResult(string(actionResendDelivery), control.DomainDelivery, "交付物已经不存在。")
	}
	if availability != delivery.Available {
		return newActionResult(string(actionResendDelivery), control.DomainDelivery, "交付物私有副本已失效，不能再次发送，也不会自动重跑原请求。")
	}
	options := []controlOption{{Label: "返回交付箱", Action: actionDeliveryPage, Page: pageNumber}}
	prompt := "交付物已再次发送\n\n名称：" + record.Title + "\n\n" + renderControlOptions(options)
	if !h.storeChoice(userID, viewDeliveryResult, options, actionDeliveryBox) {
		return newActionResult(string(actionResendDelivery), control.DomainDelivery, controlStateFailureResult().Text)
	}
	return effectActionResult(string(actionResendDelivery), control.DomainDelivery, prompt+"\n\n回复数字继续，0 返回。", EffectSendMedia, record.FilePath)
}

func formatBytes(size int64) string {
	switch {
	case size >= 1024*1024:
		return fmt.Sprintf("%.1f MiB", float64(size)/(1024*1024))
	case size >= 1024:
		return fmt.Sprintf("%.1f KiB", float64(size)/1024)
	default:
		return fmt.Sprintf("%d B", size)
	}
}
