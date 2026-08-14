package bridge

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/thread"
	"github.com/huixiangyang/codex-link-clawbot/internal/workspace"
)

func (h *Handler) openProjectCenter(_ context.Context, userID string) string {
	if h.projects == nil {
		return "Codex 工作空间未初始化。"
	}
	current := h.projects.Current(userID)
	entries := h.projects.List()
	options := make([]controlOption, 0, len(entries))
	for _, definition := range entries {
		label := definition.Name
		if definition.ID == current.ID {
			label += " · 当前"
		}
		options = append(options, controlOption{Label: label, Action: actionSelectProject, Value: definition.ID})
	}
	lines := []string{
		"Codex 工作空间",
		"",
		"这里定义微信端可查看、接管和执行 Codex 工作的受信任本机目录。",
		"当前：" + current.Name,
		"标识：" + current.ID,
		"目录：" + current.Root,
		fmt.Sprintf("入口数量：%d", len(entries)),
	}
	lines = append(lines, "", renderControlOptions(options))
	prompt := strings.Join(lines, "\n")
	if !h.storeChoice(userID, viewProjectCenter, options, actionMain) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字切换默认执行工作空间，0 返回。"
}

func (h *Handler) selectProject(userID, reference string) string {
	if h.projects == nil {
		return "Codex 工作空间未初始化。"
	}
	definition, err := h.projects.Resolve(reference)
	if err != nil {
		if errors.Is(err, workspace.ErrUnknownProject) {
			return "没有找到唯一匹配的 Codex 工作空间。发送“项目”查看可选目录。"
		}
		return fmt.Sprintf("Codex 工作空间选择失败：%v", err)
	}
	selected, err := h.projects.Select(userID, definition.ID)
	if err != nil {
		return fmt.Sprintf("Codex 工作空间选择失败：%v", err)
	}
	// 工作空间选择只更新默认执行目录；Codex 工作目录由串行协调器领取请求后设置。
	log.Printf("[project] selected project=%s for %s", selected.ID, ilink.LogLabel(userID))
	stats := sessionStats(h, userID)
	currentSession := "未创建"
	if stats.HasCurrent {
		currentSession = "已有当前线程"
	}
	options := []controlOption{
		{Label: "查看全局线程", Action: actionCodexGlobalThreadPage, Page: 1},
		{Label: "返回工作空间", Action: actionProjectCenter},
	}
	prompt := strings.Join([]string{
		"Codex 工作空间已切换",
		"",
		"当前：" + selected.Name,
		"目录：" + selected.Root,
		"线程：" + currentSession,
		"",
		renderControlOptions(options),
	}, "\n")
	if !h.storeChoice(userID, viewProjectResult, options, actionProjectCenter) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n下一条内容会进入 codex-link-clawbot 请求队列，并由 Codex 在这个默认工作空间执行；回复数字继续，0 返回。"
}

func sessionStats(h *Handler, userID string) thread.Stats {
	if h.sessions == nil {
		return thread.Stats{}
	}
	return h.sessions.Stats(userID)
}
