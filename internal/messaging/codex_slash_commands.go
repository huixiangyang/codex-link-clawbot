package messaging

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
)

const codexSlashPageSize = 6

type codexSlashSupport string

const (
	codexSlashNative       codexSlashSupport = "native"
	codexSlashAdapted      codexSlashSupport = "adapted"
	codexSlashCLIOnly      codexSlashSupport = "cli_only"
	codexSlashWindowsOnly  codexSlashSupport = "windows_only"
	codexSlashExperimental codexSlashSupport = "experimental"
)

type codexSlashCommand struct {
	Name        string
	Aliases     []string
	Label       string
	Category    string
	Support     codexSlashSupport
	Description string
	Mutating    bool
}

type codexSlashCommandGroup struct {
	Title    string
	Commands []codexSlashCommand
}

// codexSlashCommands 与官方 Codex CLI 命令表保持同一语义全集；微信不伪造 TUI 专属能力。
var codexSlashCommands = []codexSlashCommand{
	{Name: "clear", Label: "清屏并新建线程", Category: "thread", Support: codexSlashAdapted, Description: "在 codex-link-clawbot 中创建新的 Codex 线程；微信没有可清除的终端滚屏。", Mutating: true},
	{Name: "rename", Label: "重命名当前线程", Category: "thread", Support: codexSlashNative, Description: "修改当前 Codex 线程名称。", Mutating: true},
	{Name: "archive", Label: "归档当前线程", Category: "thread", Support: codexSlashNative, Description: "确认后归档当前 Codex 线程。", Mutating: true},
	{Name: "delete", Label: "永久删除当前线程", Category: "thread", Support: codexSlashNative, Description: "确认后永久删除当前线程及其派生线程。", Mutating: true},
	{Name: "compact", Label: "压缩上下文", Category: "thread", Support: codexSlashNative, Description: "调用 thread/compact/start 压缩当前线程历史。", Mutating: true},
	{Name: "copy", Label: "取回最近回答", Category: "thread", Support: codexSlashAdapted, Description: "微信没有系统剪贴板，因此返回最近一次可用结果。"},
	{Name: "diff", Label: "查看工作树差异", Category: "thread", Support: codexSlashCLIOnly, Description: "该命令依赖 Codex TUI 的差异浏览器；微信端请使用 /review 获取正式审查。"},
	{Name: "exit", Aliases: []string{"quit"}, Label: "退出 Codex CLI", Category: "thread", Support: codexSlashCLIOnly, Description: "该命令会退出交互式 CLI；codex-link-clawbot 是常驻服务，不允许微信关闭 App Server。"},
	{Name: "fork", Label: "分叉当前线程", Category: "thread", Support: codexSlashNative, Description: "调用 thread/fork 创建并切换到派生线程。", Mutating: true},
	{Name: "app", Label: "转到桌面应用", Category: "thread", Support: codexSlashCLIOnly, Description: "该命令依赖当前电脑的桌面应用与图形会话。"},
	{Name: "side", Aliases: []string{"btw"}, Label: "开启侧聊", Category: "thread", Support: codexSlashCLIOnly, Description: "侧聊是 Codex TUI 的临时界面模式，App Server 客户端没有同等稳定交互。"},
	{Name: "raw", Label: "原始滚屏模式", Category: "thread", Support: codexSlashCLIOnly, Description: "该命令只切换终端滚屏呈现。"},
	{Name: "resume", Label: "恢复已有线程", Category: "thread", Support: codexSlashNative, Description: "搜索所有受信任工作空间和 Codex 客户端来源；可以附带搜索词。"},
	{Name: "new", Label: "新建线程", Category: "thread", Support: codexSlashNative, Description: "创建新的 Codex 线程；可以附带线程名称。", Mutating: true},
	{Name: "status", Label: "查看线程状态", Category: "thread", Support: codexSlashAdapted, Description: "展示当前线程、模型、推理强度、目标和运行状态。"},

	{Name: "permissions", Label: "查看执行权限", Category: "work", Support: codexSlashAdapted, Description: "codex-link-clawbot 固定使用 never 审批与 danger-full-access 沙箱，只允许查看，不允许微信改写。"},
	{Name: "approve", Label: "批准自动审查重试", Category: "work", Support: codexSlashCLIOnly, Description: "codex-link-clawbot 使用固定 never 审批策略，没有 Codex TUI 的待批准审查界面。"},
	{Name: "model", Label: "选择模型与推理", Category: "work", Support: codexSlashNative, Description: "调用 model/list 并保存当前线程的模型与推理强度。"},
	{Name: "fast", Label: "切换快速服务层", Category: "work", Support: codexSlashCLIOnly, Description: "当前稳定 App Server 没有与 TUI /fast 等价的线程级切换接口。"},
	{Name: "plan", Label: "进入规划模式", Category: "work", Support: codexSlashCLIOnly, Description: "规划模式依赖 TUI 的 collaborationMode 交互，微信端不会把普通文本伪装成 /plan。"},
	{Name: "goal", Label: "管理线程目标", Category: "work", Support: codexSlashNative, Description: "查看、设置、暂停、继续或清除当前线程目标。", Mutating: true},
	{Name: "personality", Label: "选择沟通风格", Category: "work", Support: codexSlashCLIOnly, Description: "当前稳定 App Server 没有与 TUI 选择器同等的线程级 personality 控制。"},
	{Name: "ps", Label: "查看后台终端", Category: "work", Support: codexSlashExperimental, Description: "依赖 experimentalApi 的 thread/backgroundTerminals/list；生产客户端未启用实验协议。"},
	{Name: "stop", Aliases: []string{"clean"}, Label: "停止后台终端", Category: "work", Support: codexSlashExperimental, Description: "依赖 experimentalApi 的后台终端清理接口；不会误映射成取消 Codex 轮次。"},
	{Name: "review", Label: "审查工作树", Category: "work", Support: codexSlashNative, Description: "调用 review/start 审查未提交改动。", Mutating: true},
	{Name: "usage", Label: "查看账号与额度", Category: "work", Support: codexSlashAdapted, Description: "展示 Codex 账号、计划和 App Server 已推送的额度快照。"},

	{Name: "ide", Label: "加入 IDE 上下文", Category: "capability", Support: codexSlashCLIOnly, Description: "微信没有已连接 IDE 的当前选择和打开文件上下文。"},
	{Name: "agent", Aliases: []string{"subagents"}, Label: "切换子代理线程", Category: "capability", Support: codexSlashCLIOnly, Description: "codex-link-clawbot 当前产品边界不提供多代理线程路由。"},
	{Name: "apps", Label: "浏览应用连接器", Category: "capability", Support: codexSlashCLIOnly, Description: "应用选择器属于 Codex TUI；codex-link-clawbot 当前不在微信中授权或安装连接器。"},
	{Name: "plugins", Label: "浏览插件", Category: "capability", Support: codexSlashCLIOnly, Description: "官方 App Server 插件接口仍标记为开发中，不用于生产客户端。"},
	{Name: "hooks", Label: "查看生命周期钩子", Category: "capability", Support: codexSlashCLIOnly, Description: "钩子信任和启停属于本机配置管理，不允许从微信修改。"},
	{Name: "experimental", Label: "实验功能", Category: "capability", Support: codexSlashExperimental, Description: "实验功能可能要求重启并改变全局配置，codex-link-clawbot 生产面不开放写入。"},
	{Name: "memories", Label: "记忆设置", Category: "capability", Support: codexSlashCLIOnly, Description: "记忆注入和生成属于本机 Codex 配置，不允许从微信修改。"},
	{Name: "skills", Label: "浏览技能", Category: "capability", Support: codexSlashNative, Description: "按所有受信任工作空间调用 skills/list 并汇总启用技能。"},
	{Name: "import", Label: "导入其他代理配置", Category: "capability", Support: codexSlashCLIOnly, Description: "外部代理迁移会写入本机配置和文件，只允许在本机显式执行。"},
	{Name: "init", Label: "生成 AGENTS.md", Category: "capability", Support: codexSlashCLIOnly, Description: "该命令是本机仓库初始化写操作，微信端不伪造成普通 Codex 提示。"},
	{Name: "mcp", Label: "查看 MCP 工具", Category: "capability", Support: codexSlashAdapted, Description: "展示 Codex App Server 的全局 MCP 服务器就绪摘要。"},
	{Name: "mention", Label: "引用文件", Category: "capability", Support: codexSlashCLIOnly, Description: "Codex TUI 文件选择器不可远程映射；请直接把文件发送到微信。"},

	{Name: "keymap", Label: "终端快捷键", Category: "terminal", Support: codexSlashCLIOnly, Description: "仅修改 Codex TUI 键位。"},
	{Name: "vim", Label: "Vim 编辑模式", Category: "terminal", Support: codexSlashCLIOnly, Description: "仅切换 Codex TUI 输入框。"},
	{Name: "setup-default-sandbox", Label: "安装 Windows 沙箱", Category: "terminal", Support: codexSlashWindowsOnly, Description: "仅适用于 Windows 本机 Codex CLI。"},
	{Name: "sandbox-add-read-dir", Label: "增加沙箱只读目录", Category: "terminal", Support: codexSlashWindowsOnly, Description: "仅适用于 Windows 本机 Codex CLI；codex-link-clawbot 不接受微信提供的任意目录。"},
	{Name: "feedback", Label: "发送 Codex 反馈", Category: "terminal", Support: codexSlashCLIOnly, Description: "反馈可能上传日志，必须在本机确认范围后执行。"},
	{Name: "logout", Label: "退出 Codex 账号", Category: "terminal", Support: codexSlashCLIOnly, Description: "微信端禁止清除本机 Codex 凭据。"},
	{Name: "debug-config", Label: "调试配置层", Category: "terminal", Support: codexSlashCLIOnly, Description: "完整策略来源和本机路径只在 Codex CLI 中显示。"},
	{Name: "statusline", Label: "配置状态栏", Category: "terminal", Support: codexSlashCLIOnly, Description: "仅修改 Codex TUI 状态栏。"},
	{Name: "title", Label: "配置终端标题", Category: "terminal", Support: codexSlashCLIOnly, Description: "仅修改终端窗口标题。"},
	{Name: "theme", Label: "选择语法主题", Category: "terminal", Support: codexSlashCLIOnly, Description: "仅修改 Codex TUI 语法高亮，不等于 codex-link-clawbot 视觉风格。"},
	{Name: "pets", Aliases: []string{"pet"}, Label: "选择终端宠物", Category: "terminal", Support: codexSlashCLIOnly, Description: "仅适用于支持终端宠物的 Codex TUI。"},
}

var codexSlashByName = buildCodexSlashIndex()

func buildCodexSlashIndex() map[string]*codexSlashCommand {
	index := make(map[string]*codexSlashCommand, len(codexSlashCommands)+8)
	for commandIndex := range codexSlashCommands {
		command := &codexSlashCommands[commandIndex]
		index[command.Name] = command
		for _, alias := range command.Aliases {
			index[alias] = command
		}
	}
	return index
}

func parseCodexSlashCommand(text string) (*codexSlashCommand, string, bool) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") || strings.ContainsAny(text, "\r\n\x00") {
		return nil, "", false
	}
	fields := strings.Fields(text)
	if len(fields) == 0 || len(fields[0]) < 2 {
		return nil, "", false
	}
	name := strings.ToLower(strings.TrimPrefix(fields[0], "/"))
	command, exists := codexSlashByName[name]
	if !exists {
		return nil, "", false
	}
	argument := strings.TrimSpace(strings.TrimPrefix(text, fields[0]))
	return command, argument, true
}

func codexSlashCategories() []struct{ ID, Label string } {
	return []struct{ ID, Label string }{
		{ID: "thread", Label: "线程与会话"},
		{ID: "work", Label: "执行与工作"},
		{ID: "capability", Label: "能力与扩展"},
		{ID: "terminal", Label: "终端与账号"},
	}
}

func codexSlashCommandsForCategory(category string) []codexSlashCommand {
	commands := make([]codexSlashCommand, 0, 16)
	for _, command := range codexSlashCommands {
		if command.Category == category {
			commands = append(commands, command)
		}
	}
	return commands
}

func (support codexSlashSupport) remoteUsable() bool {
	return support == codexSlashNative || support == codexSlashAdapted
}

func codexSlashRemoteCommandsForCategory(category string) []codexSlashCommand {
	commands := make([]codexSlashCommand, 0, 12)
	for _, command := range codexSlashCommandsForCategory(category) {
		if command.Support.remoteUsable() {
			commands = append(commands, command)
		}
	}
	return commands
}

func codexSlashRemoteCategories() []struct{ ID, Label string } {
	categories := make([]struct{ ID, Label string }, 0, len(codexSlashCategories()))
	for _, category := range codexSlashCategories() {
		if len(codexSlashRemoteCommandsForCategory(category.ID)) > 0 {
			categories = append(categories, category)
		}
	}
	return categories
}

func codexSlashRemoteCommandCount() int {
	count := 0
	for _, command := range codexSlashCommands {
		if command.Support.remoteUsable() {
			count++
		}
	}
	return count
}

// codexSlashWorkbenchGroups 按微信操作路径重新分组，只返回 codex-link-clawbot 能真实执行的命令。
func codexSlashWorkbenchGroups() []codexSlashCommandGroup {
	layout := []struct {
		title string
		names []string
	}{
		{title: "会话管理", names: []string{"clear", "rename", "archive", "delete", "compact", "copy"}},
		{title: "切换与状态", names: []string{"fork", "resume", "new", "status", "permissions", "usage"}},
		{title: "模型与能力", names: []string{"model", "goal", "review", "skills", "mcp"}},
	}
	groups := make([]codexSlashCommandGroup, 0, len(layout))
	for _, item := range layout {
		group := codexSlashCommandGroup{Title: item.title, Commands: make([]codexSlashCommand, 0, len(item.names))}
		for _, name := range item.names {
			command := codexSlashByName[name]
			if command != nil && command.Name == name && command.Support.remoteUsable() {
				group.Commands = append(group.Commands, *command)
			}
		}
		groups = append(groups, group)
	}
	return groups
}

func (h *Handler) openCodexCommandCenter(userID string) string {
	options := make([]controlOption, 0, len(codexSlashRemoteCategories()))
	for _, category := range codexSlashRemoteCategories() {
		options = append(options, controlOption{
			Label: category.Label, Action: actionCodexCommandPage, Query: category.ID, Page: 1,
		})
	}
	prompt := strings.Join([]string{
		"Codex 可用命令", "",
		fmt.Sprintf("codex-link-clawbot 可用：%d 个", codexSlashRemoteCommandCount()),
		"这里只展示能够通过微信真实执行的原生或适配能力。", "",
		renderControlOptions(options),
	}, "\n")
	if !h.storeChoice(userID, viewSessionCommands, options, actionCodexDevelopment) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字查看，0 返回 Codex 开发。"
}

func (h *Handler) openCodexCommandPage(userID, category string, pageNumber int) string {
	commands := codexSlashRemoteCommandsForCategory(category)
	if len(commands) == 0 {
		return h.openCodexCommandCenter(userID)
	}
	if pageNumber <= 0 {
		pageNumber = 1
	}
	totalPages := (len(commands) + codexSlashPageSize - 1) / codexSlashPageSize
	if pageNumber > totalPages {
		pageNumber = totalPages
	}
	start := (pageNumber - 1) * codexSlashPageSize
	end := start + codexSlashPageSize
	if end > len(commands) {
		end = len(commands)
	}
	options := make([]controlOption, 0, codexSlashPageSize+2)
	for _, command := range commands[start:end] {
		label := command.Label + " · /" + command.Name
		if status := command.Support.catalogLabel(); status != "" {
			label += " · " + status
		}
		options = append(options, controlOption{
			Label: label, Action: actionCodexSlashCommand, Value: command.Name, Query: category, Page: pageNumber,
		})
	}
	if pageNumber > 1 {
		options = append(options, controlOption{Label: fmt.Sprintf("上一页 · %d/%d", pageNumber-1, totalPages), Action: actionCodexCommandPage, Query: category, Page: pageNumber - 1})
	}
	if pageNumber < totalPages {
		options = append(options, controlOption{Label: fmt.Sprintf("下一页 · %d/%d", pageNumber+1, totalPages), Action: actionCodexCommandPage, Query: category, Page: pageNumber + 1})
	}
	categoryLabel := category
	for _, current := range codexSlashRemoteCategories() {
		if current.ID == category {
			categoryLabel = current.Label
			break
		}
	}
	prompt := fmt.Sprintf("Codex 命令 · %s\n\n页码：%d / %d\n\n%s", categoryLabel, pageNumber, totalPages, renderControlOptions(options))
	if !h.storeChoice(userID, viewSessionCommandPage, options, actionCodexCommands) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字执行，0 返回可用命令。"
}

func (support codexSlashSupport) catalogLabel() string {
	switch support {
	case codexSlashCLIOnly:
		return "CLI 专属"
	case codexSlashWindowsOnly:
		return "Windows 专属"
	case codexSlashExperimental:
		return "实验协议"
	default:
		return ""
	}
}

func (h *Handler) codexSlashBoundary(userID string, command *codexSlashCommand, category string, pageNumber int) string {
	aliases := ""
	if len(command.Aliases) > 0 {
		prefixed := make([]string, len(command.Aliases))
		for index, alias := range command.Aliases {
			prefixed[index] = "/" + alias
		}
		aliases = "\n别名：" + strings.Join(prefixed, " · ")
	}
	options := []controlOption{{Label: "返回命令列表", Action: actionCodexCommandPage, Query: category, Page: pageNumber}}
	prompt := fmt.Sprintf("%s · /%s\n\n状态：%s%s\n说明：%s\n\n%s",
		command.Label, command.Name, command.Support.catalogLabel(), aliases, command.Description, renderControlOptions(options))
	if !h.storeChoice(userID, viewSessionCommandDetail, options, actionCodexCommands) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复 1 返回，0 返回命令分类。"
}

func (h *Handler) handleCodexSlashCommand(ctx context.Context, userID, text, sourceKey string, hasAttachments bool) (ActionResult, bool) {
	command, argument, exists := parseCodexSlashCommand(text)
	if !exists {
		return newActionResult("codex.slash.unknown", DomainSession, "没有这个可用的 Codex 斜杠命令。发送 / 查看 codex-link-clawbot 可直接执行的命令。"), true
	}
	if hasAttachments {
		return newActionResult("codex.slash."+command.Name, DomainSession, "斜杠命令不接收微信附件。请先执行命令，再单独发送图片或文件。"), true
	}
	if command.Mutating {
		if h.hasActiveTask(userID) {
			return newActionResult("codex.slash."+command.Name, DomainSession, mutationBusyText()), true
		}
		reserved, result := h.reserveControlReceipt(userID, sourceKey, string(actionCodexSlashCommand), DomainSession)
		if !reserved {
			return result, true
		}
	}
	textResult := h.executeCodexSlashCommand(ctx, userID, command, argument, "", 0)
	return newActionResult("codex.slash."+command.Name, DomainSession, textResult), true
}

func (h *Handler) executeCodexSlashOption(ctx context.Context, userID string, option controlOption) ActionResult {
	command := codexSlashByName[strings.ToLower(strings.TrimSpace(option.Value))]
	if command == nil {
		return invalidControlAction(option.Action, DomainSession)
	}
	if command.Mutating && h.hasActiveTask(userID) {
		return controlTextResult(option.Action, DomainSession, mutationBusyText())
	}
	text := h.executeCodexSlashCommand(ctx, userID, command, "", option.Query, option.Page)
	return controlTextResult(option.Action, DomainSession, text)
}

func (h *Handler) executeCodexSlashCommand(ctx context.Context, userID string, command *codexSlashCommand, argument, category string, pageNumber int) string {
	if command.Support != codexSlashNative && command.Support != codexSlashAdapted {
		return h.codexSlashBoundary(userID, command, category, pageNumber)
	}
	switch command.Name {
	case "clear", "new":
		if strings.TrimSpace(argument) == "" {
			return h.promptNewSessionName(userID)
		}
		return h.createSession(ctx, userID, argument)
	case "rename":
		if strings.TrimSpace(argument) == "" {
			return h.promptRenameSession(userID)
		}
		return h.renameSession(ctx, userID, argument)
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
		return h.openCodexGlobalThreadPage(ctx, userID, false, false, strings.TrimSpace(argument), 1)
	case "status":
		return h.currentSessionDetail(ctx, userID)
	case "permissions":
		return h.openCodexPermissions(userID)
	case "model":
		return h.openCodexModelOverview(ctx, userID)
	case "goal":
		return h.executeGoalSlash(ctx, userID, argument)
	case "review":
		return h.reviewCurrentThread(ctx, userID)
	case "usage":
		return h.openCodexAccount(ctx, userID)
	case "skills", "mcp":
		return h.openCodexCapabilities(ctx, userID)
	default:
		return h.codexSlashBoundary(userID, command, category, pageNumber)
	}
}

func (h *Handler) executeGoalSlash(ctx context.Context, userID, argument string) string {
	argument = strings.TrimSpace(argument)
	switch strings.ToLower(argument) {
	case "":
		return h.openCurrentThreadGoal(ctx, userID)
	case "edit":
		return h.promptCurrentThreadGoal(userID)
	case "clear":
		return h.clearCurrentThreadGoal(ctx, userID)
	case "pause":
		return h.updateCurrentThreadGoalStatus(ctx, userID, "paused")
	case "resume":
		return h.updateCurrentThreadGoalStatus(ctx, userID, "active")
	default:
		return h.setCurrentThreadGoal(ctx, userID, argument)
	}
}

func (h *Handler) openCodexPermissions(userID string) string {
	options := []controlOption{
		{Label: "查看 Codex 状态 · /status", Action: actionCurrentSession},
		{Label: "设置与诊断", Action: actionSettingsCenter},
	}
	prompt := "Codex 执行权限 · /permissions\n\n审批策略：never\n沙箱：danger-full-access\n范围：仅允许配置中的 Codex 工作空间\n\ncodex-link-clawbot 的生产权限由本机配置固定，微信只读，不能远程放宽或收紧。\n\n" + renderControlOptions(options)
	if !h.storeChoice(userID, viewSessionCommandDetail, options, actionCodexCommands) {
		return controlStateFailureResult().Text
	}
	return prompt + "\n\n回复数字操作，0 返回命令目录。"
}

func (h *Handler) openCodexUsage(ctx context.Context, userID string) string {
	lines := []string{"Codex 用量 · /usage", ""}
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
				lines = append(lines, formatSlashLimit("主额度", limits.Primary))
			}
			if limits.Secondary != nil {
				lines = append(lines, formatSlashLimit("次额度", limits.Secondary))
			}
		}
	}
	options := []controlOption{{Label: "刷新用量 · /usage", Action: actionCodexUsage}}
	lines = append(lines, "", renderControlOptions(options))
	if !h.storeChoice(userID, viewSessionCommandDetail, options, actionCodexCommands) {
		return controlStateFailureResult().Text
	}
	return strings.Join(lines, "\n") + "\n\n回复 1 刷新，0 返回命令目录。"
}

func formatSlashLimit(label string, limit *codex.RateLimitWindow) string {
	value := fmt.Sprintf("%s：已用 %d%%", label, limit.UsedPercent)
	if limit.ResetsAt != nil && *limit.ResetsAt > 0 {
		value += " · " + time.Unix(*limit.ResetsAt, 0).Format("01-02 15:04") + " 重置"
	}
	return value
}

func validateCodexSlashRegistry() error {
	seen := make(map[string]bool, len(codexSlashByName))
	categories := make(map[string]bool, len(codexSlashCategories()))
	for _, category := range codexSlashCategories() {
		categories[category.ID] = true
	}
	for _, command := range codexSlashCommands {
		if command.Name == "" || command.Label == "" || !categories[command.Category] || command.Description == "" {
			return fmt.Errorf("invalid slash command definition")
		}
		switch command.Support {
		case codexSlashNative, codexSlashAdapted, codexSlashCLIOnly, codexSlashWindowsOnly, codexSlashExperimental:
		default:
			return fmt.Errorf("invalid slash command support %s", command.Support)
		}
		for _, name := range append([]string{command.Name}, command.Aliases...) {
			if name != strings.ToLower(strings.TrimSpace(name)) || strings.ContainsAny(name, "/ \t\r\n\x00") {
				return fmt.Errorf("invalid slash command name %q", name)
			}
			if seen[name] {
				return fmt.Errorf("duplicated slash command %s", name)
			}
			seen[name] = true
		}
	}
	if len(seen) != len(codexSlashByName) {
		return fmt.Errorf("slash command index is incomplete")
	}
	return nil
}
