package messaging

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/session"
)

const (
	workbenchRecentThreadLimit = 4
	// GlobalList 本身已经扫描完整目录；首页取回扫描结果是为了把较旧的当前目标钉在紧凑列表中。
	workbenchGlobalResultLimit = 10000
)

type commandDirectorySection struct {
	Code     string
	Title    string
	Category controlOption
	Items    []controlOption
}

// openGlobalWorkbench 聚合 Codex 线程目录与 codex-link-clawbot 队列，首页只承担全局观察和高频调度。
func (h *Handler) openGlobalWorkbench(ctx context.Context, userID string) string {
	now := time.Now()
	workspaceCount := 0
	if h.projects != nil {
		workspaceCount = len(h.projects.List())
	}

	options := make([]controlOption, 0, workbenchRecentThreadLimit+20)
	recentLines := make([]string, 0, workbenchRecentThreadLimit)
	totalThreads := 0
	runningThreads := 0
	targetLine := "尚未选择 ｜ - ｜ 未连接 ｜ -"
	globalState := "不可用"

	if threadClient, err := h.sessionContext(); err == nil && h.sessions != nil && h.projects != nil && workspaceCount > 0 {
		page, listErr := h.sessions.GlobalList(ctx, userID, threadClient, h.codexWorkspaces(), false, false, "", 1, workbenchGlobalResultLimit)
		if listErr == nil {
			globalState = "就绪"
			totalThreads = page.Total
			runningThreads = page.Running
			taskStates := h.activeThreadTaskStates(userID)
			for index, item := range workbenchRecentThreads(page.Items) {
				code := fmt.Sprintf("%d", index+1)
				badge := workbenchThreadBadge(item.Current, taskStates[item.Info.ID])
				label := strings.Join([]string{
					workbenchField(threadTitle(item.Info)),
					workbenchField(item.WorkspaceName),
					formatThreadStatus(item.Info.Status),
					item.ActivityLabel(now),
					badge,
				}, " ｜ ")
				recentLines = append(recentLines, code+"  "+label)
				options = append(options, controlOption{
					Code: code, Label: label, Action: actionCodexUseGlobalThread,
					Value: item.Info.ID, Query: item.WorkspaceID,
				})
			}
		}

		if current, currentErr := h.sessions.Current(ctx, userID, threadClient); currentErr == nil {
			workspace := h.projects.Current(userID)
			targetLine = strings.Join([]string{
				workbenchField(threadTitle(current.Info)),
				workbenchField(workspace.Name),
				formatThreadStatus(current.Info.Status),
				(session.GlobalThread{Info: current.Info}).ActivityLabel(now),
			}, " ｜ ")
		}
	}

	queueState, activeRequests := h.workbenchQueueState(userID)
	quickActions := []controlOption{
		{Code: "5", Label: "全部线程 · /resume", Action: actionCodexGlobalThreadPage, Page: 1},
		{Code: "6", Label: "新建线程 · /new", Action: actionPromptNewSession},
		{Code: "7", Label: "执行与队列", Action: actionActivityPage, Page: 1},
		{Code: "8", Label: "工作空间", Action: actionProjectCenter},
		{Code: "9", Label: "刷新工作台", Action: actionMain},
	}
	options = append(options, quickActions...)
	options = append(options, workbenchDirectOptions(h.hasActiveTask(userID))...)
	if !h.storeChoiceWithTTL(userID, viewSystemMain, options, controlOption{Action: actionExit}, controlWorkbenchTTL) {
		return controlStateFailureResult().Text
	}

	lines := []string{
		"Codex 全局工作台", "",
		"从微信统筹 Codex 桌面端、CLI 与远程执行",
		"全局状态：" + globalState,
		fmt.Sprintf("工作空间：%d 个", workspaceCount),
		fmt.Sprintf("全部线程：%d 个", totalThreads),
		fmt.Sprintf("运行中：%d 个", runningThreads),
		"微信队列：" + queueState,
		"当前目标：" + targetLine,
		"", "最近线程",
	}
	if len(recentLines) == 0 {
		lines = append(lines, "当前没有可展示的 Codex 线程。")
	} else {
		lines = append(lines, recentLines...)
	}
	lines = append(lines, "", "快捷操作", renderControlOptions(quickActions))
	lines = append(lines, "", "Codex 功能")
	lines = append(lines, renderWorkbenchCodexCommands()...)
	if activeRequests > 0 {
		lines = append(lines, "", "切换目标前请先在执行与队列中处理当前微信请求。")
	}
	return strings.Join(lines, "\n") + "\n\n回复编号操作，或直接发送上方任意 /command；普通内容继续当前目标，首页 5 分钟内有效，0 退出。"
}

// workbenchDirectOptions 让原稳定能力编号在首页直接执行，不再要求先进入“全部功能”。
func workbenchDirectOptions(hasActiveTask bool) []controlOption {
	cancelLabel := "取消执行"
	if !hasActiveTask {
		cancelLabel += " · 当前无执行"
	}
	return []controlOption{
		{Code: "11", Label: "全局总览", Action: actionCodexGlobalOverview},
		{Code: "12", Label: "全局线程 · /resume", Action: actionCodexGlobalThreadPage, Page: 1},
		{Code: "13", Label: "账号与额度 · /usage", Action: actionCodexAccount},
		{Code: "21", Label: "工作空间", Action: actionProjectCenter},
		{Code: "22", Label: "目标线程 · /status", Action: actionCurrentSession},
		{Code: "23", Label: "模型与权限 · /model /permissions", Action: actionCodexModelOverview},
		{Code: "24", Label: "技能与工具 · /skills /mcp", Action: actionCodexCapabilities},
		{Code: "25", Label: "微信可用命令", Action: actionCodexCommands},
		{Code: "31", Label: "新建工作 · /new", Action: actionPromptNewSession},
		{Code: "32", Label: "审查改动 · /review", Action: actionReviewThread},
		{Code: "33", Label: "请求队列", Action: actionActivityPage, Page: 1},
		{Code: "34", Label: cancelLabel, Action: actionConfirmCancelTask},
		{Code: "41", Label: "最近结果与交付箱", Action: actionResultsDeliveryCenter},
		{Code: "42", Label: "系统健康与诊断", Action: actionDiagnosticsCenter},
		{Code: "43", Label: "呈现与安全", Action: actionSettingsCenter},
	}
}

func renderWorkbenchCodexCommands() []string {
	lines := make([]string, 0, codexSlashRemoteCommandCount()+len(codexSlashWorkbenchGroups()))
	for _, group := range codexSlashWorkbenchGroups() {
		lines = append(lines, "["+group.Title+"]")
		for _, command := range group.Commands {
			line := command.Label + " · /" + command.Name
			lines = append(lines, line)
		}
	}
	return lines
}

// workbenchRecentThreads 保留三个最新线程，并在当前目标较旧时把它钉在第四行。
func workbenchRecentThreads(items []session.GlobalThread) []session.GlobalThread {
	if len(items) <= workbenchRecentThreadLimit {
		return append([]session.GlobalThread(nil), items...)
	}
	recent := append([]session.GlobalThread(nil), items[:workbenchRecentThreadLimit]...)
	for index := workbenchRecentThreadLimit; index < len(items); index++ {
		if items[index].Current {
			recent[workbenchRecentThreadLimit-1] = items[index]
			break
		}
	}
	return recent
}

func (h *Handler) activeThreadTaskStates(userID string) map[string]string {
	states := make(map[string]string)
	priorities := make(map[string]int)
	if h.tasks == nil {
		return states
	}
	for _, task := range h.tasks.List(userID) {
		if task.ThreadID == "" {
			continue
		}
		label := ""
		priority := 0
		switch task.State {
		case "running":
			label = "微信执行中"
			priority = 3
		case "delivering":
			label = "微信发送中"
			priority = 2
		case "queued":
			label = "微信等待中"
			priority = 1
		}
		if label != "" && priority > priorities[task.ThreadID] {
			states[task.ThreadID] = label
			priorities[task.ThreadID] = priority
		}
	}
	return states
}

func (h *Handler) workbenchQueueState(userID string) (string, int) {
	if h.tasks == nil {
		return "空闲", 0
	}
	status := h.tasks.Status(userID)
	active := status.Queued + status.Running + status.Delivering
	if active == 0 {
		return "空闲", 0
	}
	parts := make([]string, 0, 3)
	if status.Running > 0 {
		parts = append(parts, fmt.Sprintf("%d 执行", status.Running))
	}
	if status.Queued > 0 {
		parts = append(parts, fmt.Sprintf("%d 等待", status.Queued))
	}
	if status.Delivering > 0 {
		parts = append(parts, fmt.Sprintf("%d 发送", status.Delivering))
	}
	return strings.Join(parts, " · "), active
}

func workbenchThreadBadge(current bool, taskState string) string {
	badges := make([]string, 0, 2)
	if current {
		badges = append(badges, "当前目标")
	}
	if taskState != "" {
		badges = append(badges, taskState)
	}
	if len(badges) == 0 {
		return "普通"
	}
	return strings.Join(badges, " / ")
}

func workbenchField(value string) string {
	return strings.ReplaceAll(strings.TrimSpace(value), "｜", "/")
}

// openCommandDirectory 生成稳定的二级功能目录；运行状态只改变标签，不改变编号。
func (h *Handler) openCommandDirectory(ctx context.Context, userID string) string {
	currentSession := "未选择"
	if h.sessions != nil {
		if threadAgent, err := h.sessionContext(); err == nil {
			if current, currentErr := h.sessions.Current(ctx, userID, threadAgent); currentErr == nil {
				currentSession = threadTitle(current.Info)
			} else if !errors.Is(currentErr, session.ErrNoActive) {
				currentSession = "暂不可读"
			}
		}
	}

	projectName := "未配置"
	workspaceCount := 0
	if h.projects != nil {
		current := h.projects.Current(userID)
		projectName = current.Name
		workspaceCount = len(h.projects.List())
	}

	taskState := "空闲"
	if h.tasks != nil {
		status := h.tasks.Status(userID)
		if status.Running > 0 {
			taskState = "运行中"
		} else if status.Delivering > 0 {
			taskState = "发送中"
		} else if status.Queued > 0 {
			taskState = "等待中"
		}
	}

	cancelLabel := "取消执行"
	if !h.hasActiveTask(userID) {
		cancelLabel += " · 当前无执行"
	}

	sections := []commandDirectorySection{
		{
			Code: "1", Title: "Codex · 全局",
			Category: controlOption{Code: "1", Label: "Codex · 全局", Action: actionCodexGlobalOverview},
			Items: []controlOption{
				{Code: "11", Label: "全局总览", Action: actionCodexGlobalOverview},
				{Code: "12", Label: "全局线程 · /resume", Action: actionCodexGlobalThreadPage, Page: 1},
				{Code: "13", Label: "账号与额度 · /usage", Action: actionCodexAccount},
			},
		},
		{
			Code: "2", Title: "Codex · 工作空间",
			Category: controlOption{Code: "2", Label: "Codex · 工作空间", Action: actionProjectCenter},
			Items: []controlOption{
				{Code: "21", Label: "工作空间", Action: actionProjectCenter},
				{Code: "22", Label: "目标线程 · /status", Action: actionCurrentSession},
				{Code: "23", Label: "模型与权限 · /model /permissions", Action: actionCodexModelOverview},
				{Code: "24", Label: "技能与工具 · /skills /mcp", Action: actionCodexCapabilities},
				{Code: "25", Label: "微信可用命令", Action: actionCodexCommands},
			},
		},
		{
			Code: "3", Title: "Codex · 执行",
			Category: controlOption{Code: "3", Label: "Codex · 执行", Action: actionActivityPage, Page: 1},
			Items: []controlOption{
				{Code: "31", Label: "新建工作 · /new", Action: actionPromptNewSession},
				{Code: "32", Label: "审查改动 · /review", Action: actionReviewThread},
				{Code: "33", Label: "请求队列", Action: actionActivityPage, Page: 1},
				{Code: "34", Label: cancelLabel, Action: actionConfirmCancelTask},
			},
		},
		{
			Code: "4", Title: "codex-link-clawbot · 远程",
			Category: controlOption{Code: "4", Label: "codex-link-clawbot · 远程", Action: actionResultsDeliveryCenter},
			Items: []controlOption{
				{Code: "41", Label: "最近结果与交付箱", Action: actionResultsDeliveryCenter},
				{Code: "42", Label: "系统健康与诊断", Action: actionDiagnosticsCenter},
				{Code: "43", Label: "呈现与安全", Action: actionSettingsCenter},
			},
		},
	}

	options := make([]controlOption, 0, 19)
	lines := []string{
		"Codex 全部功能", "",
		"按领域浏览 Codex 与 codex-link-clawbot 控制能力",
		fmt.Sprintf("工作空间：%d 个 · 当前 %s", workspaceCount, projectName),
		"目标线程：" + currentSession,
		"codex-link-clawbot 执行：" + taskState,
	}
	for _, section := range sections {
		options = append(options, section.Category)
		options = append(options, section.Items...)
		lines = append(lines, "", fmt.Sprintf("[%s]  %s", section.Code, section.Title), renderControlOptions(section.Items))
	}
	if !h.storeChoiceWithTTL(userID, viewSystemMain, options, controlOption{Action: actionMain}, controlDirectoryTTL) {
		return controlStateFailureResult().Text
	}
	return strings.Join(lines, "\n") + "\n\n回复编号或直接发送 /command，0 返回全局工作台；功能目录 30 分钟内有效。"
}
