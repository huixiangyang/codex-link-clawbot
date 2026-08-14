package bridge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/control"
)

const codexCommandPageSize = 6

type codexCommandSupport string

const (
	codexCommandNative  codexCommandSupport = "native"
	codexCommandAdapted codexCommandSupport = "adapted"
)

type codexCommandDefinition struct {
	Name        string
	Label       string
	Category    string
	Support     codexCommandSupport
	Description string
	Mutating    bool
}

// codexCommandCatalog 只登记微信数字菜单能够真实执行的能力。
var codexCommandCatalog = []codexCommandDefinition{
	{Name: "clear", Label: "清屏并新建线程", Category: "thread", Support: codexCommandAdapted, Description: "在 codex-link-clawbot 中创建新的 Codex 线程；微信没有可清除的终端滚屏。", Mutating: true},
	{Name: "rename", Label: "重命名当前线程", Category: "thread", Support: codexCommandNative, Description: "修改当前 Codex 线程名称。", Mutating: true},
	{Name: "archive", Label: "归档当前线程", Category: "thread", Support: codexCommandNative, Description: "确认后归档当前 Codex 线程。", Mutating: true},
	{Name: "delete", Label: "永久删除当前线程", Category: "thread", Support: codexCommandNative, Description: "确认后永久删除当前线程及其派生线程。", Mutating: true},
	{Name: "compact", Label: "压缩上下文", Category: "thread", Support: codexCommandNative, Description: "调用 thread/compact/start 压缩当前线程历史。", Mutating: true},
	{Name: "copy", Label: "取回最近回答", Category: "thread", Support: codexCommandAdapted, Description: "微信没有系统剪贴板，因此返回最近一次可用结果。"},
	{Name: "fork", Label: "分叉当前线程", Category: "thread", Support: codexCommandNative, Description: "调用 thread/fork 创建并切换到派生线程。", Mutating: true},
	{Name: "resume", Label: "恢复已有线程", Category: "thread", Support: codexCommandNative, Description: "搜索所有受信任工作空间和 Codex 客户端来源。"},
	{Name: "new", Label: "新建线程", Category: "thread", Support: codexCommandNative, Description: "创建新的 Codex 线程。", Mutating: true},
	{Name: "status", Label: "查看线程状态", Category: "thread", Support: codexCommandAdapted, Description: "展示当前线程、模型、推理强度、目标和运行状态。"},

	{Name: "permissions", Label: "查看执行权限", Category: "work", Support: codexCommandAdapted, Description: "codex-link-clawbot 固定使用 never 审批与 danger-full-access 沙箱，只允许查看，不允许微信改写。"},
	{Name: "model", Label: "选择模型与推理", Category: "work", Support: codexCommandNative, Description: "调用 model/list 并保存当前线程的模型与推理强度。"},
	{Name: "goal", Label: "管理线程目标", Category: "work", Support: codexCommandNative, Description: "查看、设置、暂停、继续或清除当前线程目标。", Mutating: true},
	{Name: "review", Label: "审查工作树", Category: "work", Support: codexCommandNative, Description: "调用 review/start 审查未提交改动。", Mutating: true},
	{Name: "usage", Label: "查看账号与额度", Category: "work", Support: codexCommandAdapted, Description: "展示 Codex 账号、计划和 App Server 已推送的额度快照。"},

	{Name: "skills", Label: "浏览技能", Category: "capability", Support: codexCommandNative, Description: "按所有受信任工作空间调用 skills/list 并汇总启用技能。"},
	{Name: "mcp", Label: "查看 MCP 工具", Category: "capability", Support: codexCommandAdapted, Description: "展示 Codex App Server 的全局 MCP 服务器就绪摘要。"},
}

var codexCommandByName = buildCodexCommandIndex()

func buildCodexCommandIndex() map[string]*codexCommandDefinition {
	index := make(map[string]*codexCommandDefinition, len(codexCommandCatalog))
	for commandIndex := range codexCommandCatalog {
		command := &codexCommandCatalog[commandIndex]
		index[command.Name] = command
	}
	return index
}

func codexCommandCategories() []struct{ ID, Label string } {
	return []struct{ ID, Label string }{
		{ID: "thread", Label: "线程与会话"},
		{ID: "work", Label: "执行与工作"},
		{ID: "capability", Label: "能力与扩展"},
	}
}

func codexCommandsForCategory(category string) []codexCommandDefinition {
	commands := make([]codexCommandDefinition, 0, 10)
	for _, command := range codexCommandCatalog {
		if command.Category == category {
			commands = append(commands, command)
		}
	}
	return commands
}

func codexCommandCount() int {
	return len(codexCommandCatalog)
}

func codexCommandByLabel(label string) *codexCommandDefinition {
	label = strings.TrimSpace(label)
	for commandIndex := range codexCommandCatalog {
		if codexCommandCatalog[commandIndex].Label == label {
			return &codexCommandCatalog[commandIndex]
		}
	}
	return nil
}

func (h *Handler) openCodexCommandCenter(userID string) string {
	options := make([]controlOption, 0, len(codexCommandCategories()))
	for _, category := range codexCommandCategories() {
		options = append(options, controlOption{
			Label: category.Label, Action: actionCodexCommandPage, Query: category.ID, Page: 1,
		})
	}
	prompt := strings.Join([]string{
		"Codex 操作", "",
		fmt.Sprintf("codex-link-clawbot 可用：%d 个", codexCommandCount()),
		"这里只展示能够通过微信数字菜单真实执行的能力。", "",
		renderControlOptions(options),
	}, "\n")
	if !h.storeChoice(userID, viewSessionCommands, options, actionCodexDevelopment) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字查看，0 返回 Codex 开发。"
}

func (h *Handler) openCodexCommandPage(userID, category string, pageNumber int) string {
	commands := codexCommandsForCategory(category)
	if len(commands) == 0 {
		return h.openCodexCommandCenter(userID)
	}
	if pageNumber <= 0 {
		pageNumber = 1
	}
	totalPages := (len(commands) + codexCommandPageSize - 1) / codexCommandPageSize
	if pageNumber > totalPages {
		pageNumber = totalPages
	}
	start := (pageNumber - 1) * codexCommandPageSize
	end := start + codexCommandPageSize
	if end > len(commands) {
		end = len(commands)
	}
	options := make([]controlOption, 0, codexCommandPageSize+2)
	for _, command := range commands[start:end] {
		options = append(options, controlOption{
			Label: command.Label, Action: actionCodexCommand, Value: command.Name, Query: category, Page: pageNumber,
		})
	}
	if pageNumber > 1 {
		options = append(options, controlOption{Label: fmt.Sprintf("上一页 · %d/%d", pageNumber-1, totalPages), Action: actionCodexCommandPage, Query: category, Page: pageNumber - 1})
	}
	if pageNumber < totalPages {
		options = append(options, controlOption{Label: fmt.Sprintf("下一页 · %d/%d", pageNumber+1, totalPages), Action: actionCodexCommandPage, Query: category, Page: pageNumber + 1})
	}
	categoryLabel := category
	for _, current := range codexCommandCategories() {
		if current.ID == category {
			categoryLabel = current.Label
			break
		}
	}
	prompt := fmt.Sprintf("Codex 操作 · %s\n\n页码：%d / %d\n\n%s", categoryLabel, pageNumber, totalPages, renderControlOptions(options))
	if !h.storeChoice(userID, viewSessionCommandPage, options, actionCodexCommands) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字执行，0 返回可用命令。"
}

func (h *Handler) executeCodexCommandOption(ctx context.Context, userID string, option controlOption) ActionResult {
	command := codexCommandByName[strings.ToLower(strings.TrimSpace(option.Value))]
	if command == nil {
		return invalidControlAction(option.Action, control.DomainSession)
	}
	if command.Mutating && h.hasActiveTask(userID) {
		return controlTextResult(option.Action, control.DomainSession, mutationBusyText())
	}
	if command.Name == "review" {
		return pageActionResult(string(option.Action), control.DomainSession, h.reviewCurrentThreadPage(ctx, userID))
	}
	text := h.executeCodexCommand(ctx, userID, command)
	return controlTextResult(option.Action, control.DomainSession, text)
}

func (h *Handler) executeCodexCommand(ctx context.Context, userID string, command *codexCommandDefinition) string {
	switch command.Name {
	case "clear", "new":
		return h.promptNewSessionName(userID)
	case "rename":
		return h.promptRenameSession(userID)
	case "archive":
		return h.confirmArchiveCurrent(ctx, userID)
	case "delete":
		return h.confirmDeleteCurrentThread(ctx, userID)
	case "compact":
		return h.compactCurrentThread(ctx, userID)
	case "copy":
		if task, exists := h.latestSuccessfulTask(userID, false); exists {
			return h.openActivityDetail(userID, task.ID, 1)
		}
		return "最近还没有成功执行记录。直接发送内容即可开始。"
	case "fork":
		return h.forkCurrentThread(ctx, userID)
	case "resume":
		return h.openCodexGlobalThreadPage(ctx, userID, false, false, "", 1)
	case "status":
		return h.currentSessionDetail(ctx, userID)
	case "permissions":
		return h.openCodexPermissions(userID)
	case "model":
		return h.openCodexModelOverview(ctx, userID)
	case "goal":
		return h.openCurrentThreadGoal(ctx, userID)
	case "usage":
		return h.openCodexAccount(ctx, userID)
	case "skills", "mcp":
		return h.openCodexCapabilities(ctx, userID)
	default:
		return "这个操作已经失效。发送“菜单”重新打开操作总览。"
	}
}

func (h *Handler) openCodexPermissions(userID string) string {
	options := []controlOption{
		{Label: "查看 Codex 状态", Action: actionCurrentSession},
		{Label: "设置与诊断", Action: actionSettingsCenter},
	}
	prompt := "Codex 执行权限\n\n审批策略：never\n沙箱：danger-full-access\n范围：仅允许配置中的 Codex 工作空间\n\ncodex-link-clawbot 的生产权限由本机配置固定，微信只读，不能远程放宽或收紧。\n\n" + renderControlOptions(options)
	if !h.storeChoice(userID, viewSessionCommandDetail, options, actionCodexCommands) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字操作，0 返回命令目录。"
}

func (h *Handler) openCodexUsage(ctx context.Context, userID string) string {
	lines := []string{"Codex 用量", ""}
	var threadID string
	if h.sessions != nil {
		if threadAgent, err := h.sessionContext(); err == nil {
			if current, currentErr := h.sessions.Current(ctx, userID, threadAgent); currentErr == nil {
				threadID = current.Info.ID
				lines = append(lines, "当前线程："+threadTitle(current.Info))
			}
		}
	}
	usageProvider, ok := h.codex.(codex.UsageProvider)
	if !ok {
		lines = append(lines, "用量快照：当前 App Server 未提供")
	} else {
		usageFound := false
		if threadID != "" {
			if usage, exists := usageProvider.Usage(threadID); exists {
				usageFound = true
				lines = append(lines,
					fmt.Sprintf("最近轮次：%d 个令牌", usage.Last.TotalTokens),
					fmt.Sprintf("线程累计：%d 个令牌", usage.Total.TotalTokens),
				)
				if usage.ModelContextWindow != nil {
					lines = append(lines, fmt.Sprintf("上下文窗口：%d 个令牌", *usage.ModelContextWindow))
				}
			}
		}
		if !usageFound {
			lines = append(lines, "线程用量：尚无快照")
		}
		if limits, exists := usageProvider.RateLimits(); exists {
			if limits.Primary != nil {
				lines = append(lines, formatRateLimit("主额度", limits.Primary))
			}
			if limits.Secondary != nil {
				lines = append(lines, formatRateLimit("次额度", limits.Secondary))
			}
		}
	}
	options := []controlOption{{Label: "刷新用量", Action: actionCodexUsage}}
	lines = append(lines, "", renderControlOptions(options))
	if !h.storeChoice(userID, viewSessionCommandDetail, options, actionCodexCommands) {
		return controlStateFailureResult().Text
	}
	return strings.Join(lines, "\n") + "\n\n回复 1 刷新，0 返回命令目录。"
}

func formatRateLimit(label string, limit *codex.RateLimitWindow) string {
	value := fmt.Sprintf("%s：已用 %d%%", label, limit.UsedPercent)
	if limit.ResetsAt != nil && *limit.ResetsAt > 0 {
		value += " · " + time.Unix(*limit.ResetsAt, 0).Format("01-02 15:04") + " 重置"
	}
	return value
}

func validateCodexCommandCatalog() error {
	seen := make(map[string]bool, len(codexCommandByName))
	categories := make(map[string]bool, len(codexCommandCategories()))
	for _, category := range codexCommandCategories() {
		categories[category.ID] = true
	}
	for _, command := range codexCommandCatalog {
		if command.Name == "" || command.Label == "" || !categories[command.Category] || command.Description == "" {
			return fmt.Errorf("invalid command definition")
		}
		switch command.Support {
		case codexCommandNative, codexCommandAdapted:
		default:
			return fmt.Errorf("invalid command support %s", command.Support)
		}
		name := command.Name
		if name != strings.ToLower(strings.TrimSpace(name)) || strings.ContainsAny(name, "/ \t\r\n\x00") {
			return fmt.Errorf("invalid command name %q", name)
		}
		if seen[name] {
			return fmt.Errorf("duplicated command %s", name)
		}
		seen[name] = true
	}
	if len(seen) != len(codexCommandByName) {
		return fmt.Errorf("command index is incomplete")
	}
	return nil
}
