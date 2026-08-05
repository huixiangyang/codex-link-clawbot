package messaging

import (
	"fmt"
	"strings"
	"time"
)

func (h *Handler) openLibraryCenter(userID string) string {
	if h.library == nil {
		return "素材与交付中心当前不可用。"
	}
	links := h.library.List(userID, LibraryLink)
	deliveries := h.library.List(userID, LibraryDelivery)
	options := []controlOption{
		{Label: fmt.Sprintf("链接素材 · %d", len(links)), Action: actionLibraryPage, Query: string(LibraryLink), Page: 1},
		{Label: fmt.Sprintf("交付记录 · %d", len(deliveries)), Action: actionLibraryPage, Query: string(LibraryDelivery), Page: 1},
	}
	prompt := strings.Join([]string{
		"素材与交付", "",
		fmt.Sprintf("链接：%d", len(links)),
		fmt.Sprintf("交付物：%d", len(deliveries)),
		"Codex 新交付物会保存私有副本，之后可从微信再次发送。",
		"", renderControlOptions(options),
	}, "\n")
	h.storeChoice(userID, prompt, options, actionMore)
	return prompt + "\n\n回复数字查看，0 返回更多功能。"
}

func (h *Handler) openLibraryPage(userID string, kind LibraryKind, pageNumber int) string {
	if h.library == nil {
		return "素材与交付中心当前不可用。"
	}
	if kind != LibraryLink && kind != LibraryDelivery {
		return "素材分类已经失效。"
	}
	records := h.library.List(userID, kind)
	title := libraryKindName(kind)
	if len(records) == 0 {
		return title + "\n\n当前没有记录。"
	}
	if pageNumber <= 0 {
		pageNumber = 1
	}
	totalPages := (len(records) + controlSessionPageSize - 1) / controlSessionPageSize
	if pageNumber > totalPages {
		return fmt.Sprintf("记录页面不存在：%d / %d。", pageNumber, totalPages)
	}
	start := (pageNumber - 1) * controlSessionPageSize
	end := start + controlSessionPageSize
	if end > len(records) {
		end = len(records)
	}
	options := make([]controlOption, 0, controlSessionPageSize+2)
	for _, record := range records[start:end] {
		options = append(options, controlOption{
			Label: normalizeSessionLine(record.Title, 42), Action: actionLibraryDetail,
			Value: record.ID, Query: string(kind), Page: pageNumber,
		})
	}
	if pageNumber > 1 {
		options = append(options, controlOption{Label: fmt.Sprintf("上一页 · %d/%d", pageNumber-1, totalPages), Action: actionLibraryPage, Query: string(kind), Page: pageNumber - 1})
	}
	if pageNumber < totalPages {
		options = append(options, controlOption{Label: fmt.Sprintf("下一页 · %d/%d", pageNumber+1, totalPages), Action: actionLibraryPage, Query: string(kind), Page: pageNumber + 1})
	}
	prompt := strings.Join([]string{
		title, "", fmt.Sprintf("页码：%d / %d", pageNumber, totalPages), fmt.Sprintf("记录：%d", len(records)), "", renderControlOptions(options),
	}, "\n")
	h.storeChoiceWithBack(userID, prompt, options, controlOption{Action: actionLibraryCenter})
	return prompt + "\n\n回复数字查看详情，0 返回素材与交付中心。"
}

func (h *Handler) openLibraryDetail(userID, id string, kind LibraryKind, pageNumber int) string {
	if h.library == nil {
		return "素材与交付中心当前不可用。"
	}
	record, exists := h.library.Find(userID, id)
	if !exists || record.Kind != kind {
		return "这条记录已经变化，请刷新列表。"
	}
	lines := []string{
		libraryKindName(kind) + "详情", "", "名称：" + record.Title,
		"项目：" + emptyAutomationValue(record.ProjectID, "未关联"),
		"时间：" + time.Unix(record.CreatedAt, 0).Local().Format("2006-01-02 15:04"),
	}
	options := []controlOption{{Label: "返回列表", Action: actionLibraryPage, Query: string(kind), Page: pageNumber}}
	if kind == LibraryLink {
		lines = append(lines, "链接："+record.URL)
	} else {
		lines = append(lines, "大小："+formatBytes(record.Size))
		options = append([]controlOption{{Label: "再次发送", Action: actionResendDelivery, Value: record.ID, Page: pageNumber}}, options...)
	}
	lines = append(lines, "", renderControlOptions(options))
	prompt := strings.Join(lines, "\n")
	h.storeChoiceWithBack(userID, prompt, options, controlOption{Action: actionLibraryPage, Query: string(kind), Page: pageNumber})
	return prompt + "\n\n回复数字操作，0 返回原列表。"
}

func (h *Handler) resendDelivery(userID, id string, pageNumber int) ActionResult {
	if h.library == nil {
		return newActionResult(string(actionResendDelivery), DomainLibrary, "交付记录当前不可用。")
	}
	record, exists := h.library.Find(userID, id)
	if !exists || record.Kind != LibraryDelivery {
		return newActionResult(string(actionResendDelivery), DomainLibrary, "交付物已经不存在。")
	}
	options := []controlOption{{Label: "返回交付记录", Action: actionLibraryPage, Query: string(LibraryDelivery), Page: pageNumber}}
	prompt := "交付物已再次发送\n\n名称：" + record.Title + "\n\n" + renderControlOptions(options)
	h.storeChoice(userID, prompt, options, actionLibraryCenter)
	return effectActionResult(string(actionResendDelivery), DomainLibrary, prompt+"\n\n回复数字继续，0 返回。", EffectSendMedia, record.FilePath)
}

func libraryKindName(kind LibraryKind) string {
	if kind == LibraryDelivery {
		return "交付记录"
	}
	return "链接素材"
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
