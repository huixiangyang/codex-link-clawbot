package bridge

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/huixiangyang/codex-link-clawbot/internal/codex"
	"github.com/huixiangyang/codex-link-clawbot/internal/thread"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
	"github.com/huixiangyang/codex-link-clawbot/internal/workspace"
)

const threadRelationChildLimit = 5

func (h *Handler) codexWorkspaces() []thread.Workspace {
	if h.projects == nil {
		return nil
	}
	definitions := h.projects.List()
	workspaces := make([]thread.Workspace, 0, len(definitions))
	for _, definition := range definitions {
		workspaces = append(workspaces, thread.Workspace{
			ID: definition.ID, Name: definition.Name, Root: definition.Root,
		})
	}
	return workspaces
}

func (h *Handler) openCodexGlobalOverview(ctx context.Context, userID string) string {
	threadClient, err := h.sessionContext()
	if err != nil || h.projects == nil {
		return "Codex 全局控制面当前不可用。"
	}
	active, err := h.sessions.GlobalList(ctx, userID, threadClient, h.codexWorkspaces(), false, false, "", 1, 1)
	if err != nil {
		return "读取 Codex 全局线程失败：" + err.Error()
	}
	archived, err := h.sessions.GlobalList(ctx, userID, threadClient, h.codexWorkspaces(), true, false, "", 1, 1)
	if err != nil {
		return "读取 Codex 归档线程失败：" + err.Error()
	}
	loaded := active.Loaded
	if global, ok := h.codex.(codex.GlobalControlClient); ok {
		if ids, loadedErr := global.ListLoadedThreadIDs(ctx); loadedErr == nil {
			loaded = len(ids)
		}
	}
	target := "未选择"
	currentWorkspace := h.projects.Current(userID)
	if current, currentErr := h.sessions.Current(ctx, userID, threadClient); currentErr == nil {
		target = threadTitle(current.Info) + " · " + currentWorkspace.Name
	}
	runtime := h.codex.Info()
	lines := []string{
		"Codex 全局总览", "",
		fmt.Sprintf("工作空间：%d 个", len(h.projects.List())),
		fmt.Sprintf("活动线程：%d 个", active.Total),
		fmt.Sprintf("运行中：%d 个", active.Running),
		fmt.Sprintf("已加载：%d 个", loaded),
		fmt.Sprintf("已归档：%d 个", archived.Total),
		"目标线程：" + target,
		fmt.Sprintf("App Server：运行中 · PID %d", runtime.PID),
	}
	options := []controlOption{
		{Label: "运行中线程", Action: actionCodexGlobalThreadPage, Page: 1, AutoUse: true},
		{Label: "全部线程", Action: actionCodexGlobalThreadPage, Page: 1},
		{Label: "账号与额度", Action: actionCodexAccount},
		{Label: "目标线程", Action: actionCurrentSession},
	}
	lines = append(lines, "", renderControlOptions(options))
	if !h.storeChoice(userID, viewCodexGlobalOverview, options, actionMain) {
		return controlStateFailureResult().Text
	}
	return strings.Join(lines, "\n") + "\n\n回复数字操作，0 返回全局首页。"
}

func (h *Handler) openCodexGlobalThreadPage(ctx context.Context, userID string, archived, runningOnly bool, query string, pageNumber int) string {
	threadClient, err := h.sessionContext()
	if err != nil || h.projects == nil {
		return "Codex 全局线程目录当前不可用。"
	}
	page, err := h.sessions.GlobalList(ctx, userID, threadClient, h.codexWorkspaces(), archived, runningOnly, query, pageNumber, controlSessionPageSize)
	if err != nil {
		return "读取 Codex 全局线程失败：" + err.Error()
	}
	options := make([]controlOption, 0, len(page.Items)+2)
	for _, item := range page.Items {
		label := threadTitle(item.Info) + " · " + item.WorkspaceName + " · " + formatThreadStatus(item.Info.Status)
		if item.Current {
			label += " · 当前目标"
		}
		options = append(options, controlOption{
			Label: label, Action: actionCodexUseGlobalThread,
			Value: item.Info.ID, Query: item.WorkspaceID,
		})
	}
	if page.Number > 1 {
		options = append(options, controlOption{
			Label:  fmt.Sprintf("上一页 · %d/%d", page.Number-1, page.TotalPages),
			Action: actionCodexGlobalThreadPage, Page: page.Number - 1,
			Archived: archived, AutoUse: runningOnly, Query: query, Navigate: navigationPrevious,
		})
	}
	if page.Number < page.TotalPages {
		options = append(options, controlOption{
			Label:  fmt.Sprintf("下一页 · %d/%d", page.Number+1, page.TotalPages),
			Action: actionCodexGlobalThreadPage, Page: page.Number + 1,
			Archived: archived, AutoUse: runningOnly, Query: query, Navigate: navigationNext,
		})
	}
	if !runningOnly {
		options = append(options, controlOption{Label: "仅看运行中线程", Action: actionCodexGlobalThreadPage, Page: 1, AutoUse: true})
	}
	if archived || runningOnly || strings.TrimSpace(query) != "" {
		options = append(options, controlOption{Label: "返回全部线程", Action: actionCodexGlobalThreadPage, Page: 1})
	}
	options = append(options, controlOption{Label: "搜索线程", Action: actionPromptGlobalSearch})
	if !archived {
		options = append(options, controlOption{Label: "已归档线程", Action: actionCodexGlobalThreadPage, Page: 1, Archived: true})
	}
	title := "Codex 全部线程"
	if runningOnly {
		title = "Codex 运行中线程"
	} else if archived {
		title = "Codex 已归档线程"
	} else if strings.TrimSpace(query) != "" {
		title = "Codex 全局搜索"
	}
	lines := []string{
		title, "",
		fmt.Sprintf("页码：%d / %d", page.Number, page.TotalPages),
		fmt.Sprintf("匹配：%d 个", page.Total),
		"范围：所有受信任工作空间 · 所有 Codex 客户端来源",
	}
	if strings.TrimSpace(query) != "" {
		lines = append(lines, "搜索："+normalizeSessionLine(query, 80))
	}
	if len(page.Items) == 0 {
		lines = append(lines, "", "当前范围没有线程。", "", renderControlOptions(options))
	} else {
		lines = append(lines, "", renderControlOptions(options))
	}
	if !h.storeChoice(userID, viewCodexGlobalThreads, options, actionCodexGlobalOverview) {
		return controlStateFailureResult().Text
	}
	return strings.Join(lines, "\n") + "\n\n回复线程编号即可设为目标，0 返回全局总览。"
}

func (h *Handler) promptCodexGlobalSearch(userID string) string {
	prompt := "搜索 Codex 全局线程\n\n发送名称、摘要或线程编号；搜索范围覆盖所有受信任工作空间和 Codex 客户端来源。回复 0 返回。"
	if !h.storeInput(userID, viewCodexGlobalSearch, controlGlobalThreadSearch, actionCodexGlobalOverview) {
		return controlStateFailureResult().Text
	}
	return prompt
}

func (h *Handler) useCodexGlobalThread(ctx context.Context, userID, workspaceID, threadID string) string {
	if h.projects == nil || h.sessions == nil {
		return "Codex 全局线程控制面当前不可用。"
	}
	definition, exists := h.projects.Get(strings.TrimSpace(workspaceID))
	if !exists {
		return "这个工作空间已经不在受信任列表中。"
	}
	threadClient, err := h.sessionContext()
	if err != nil {
		return err.Error()
	}
	info, err := threadClient.ReadThread(ctx, strings.TrimSpace(threadID))
	if err != nil {
		return "读取目标线程失败：" + err.Error()
	}
	if !threadBelongsToWorkspace(info.Cwd, definition) {
		return "目标线程的工作目录不在指定受信任工作空间中，已拒绝接管。"
	}
	selected, err := h.sessions.UseGlobalThread(ctx, userID, thread.Workspace{ID: definition.ID, Name: definition.Name, Root: definition.Root}, info.ID, threadClient)
	if err != nil {
		return formatSessionError(err)
	}
	if _, err := h.projects.Select(userID, definition.ID); err != nil {
		return "目标线程已加载，但工作空间焦点保存失败：" + err.Error()
	}
	return h.sessionSuccess(userID, "已从 Codex 全局目录接管目标线程。\n工作空间："+definition.Name, selected)
}

// buildCurrentThreadRelations 即时读取 Codex 目录中的一层分叉关系，不使用本地焦点索引补造树结构。
func (h *Handler) buildCurrentThreadRelations(ctx context.Context, userID string) controlPage {
	threadClient, err := h.sessionContext()
	if err != nil || h.projects == nil || h.sessions == nil {
		return textControlPage("Codex 线程关系当前不可用。")
	}
	relations, err := h.sessions.CurrentRelations(ctx, userID, threadClient, h.codexWorkspaces(), threadRelationChildLimit)
	if err != nil {
		if errors.Is(err, thread.ErrNoActive) {
			return textControlPage("当前没有目标线程。请先从全局线程目录接管一个线程。")
		}
		return textControlPage("读取 Codex 线程关系失败。请确认 Codex 可用并刷新；底层路径和错误细节不会发送到微信。")
	}

	options := make([]controlOption, 0, len(relations.Children)+3)
	lines := []string{
		"Codex 线程关系图", "",
		"工作空间：" + relations.Current.WorkspaceName,
		"当前目标：" + strings.Join([]string{
			threadRelationTitle(relations.Current),
			threadRelationField(relations.Current.WorkspaceName),
			formatThreadStatus(relations.Current.Status),
		}, " ｜ "),
	}
	threadMap := &visual.ThreadMap{
		Workspace: relations.Current.WorkspaceName,
		Current: visual.ThreadMapNode{
			Role: visual.ThreadMapCurrent, Title: threadRelationTitle(relations.Current),
			Workspace: threadRelationField(relations.Current.WorkspaceName), Status: formatThreadStatus(relations.Current.Status),
		},
		ParentUnavailable: relations.ParentUnavailable, Truncated: relations.Truncated,
		Actions: []visual.ThreadMapAction{
			{Code: "8", Label: "刷新关系图", Icon: "rotate-ccw"},
			{Code: "9", Label: "全部线程", Icon: "list-tree"},
		},
		Footer: "回复关系节点编号即可接管 · 0 返回当前线程 · 关系状态 10 分钟内有效",
	}
	switch {
	case relations.Parent != nil:
		lines = append(lines, "父线程：可接管")
	case relations.ParentUnavailable:
		lines = append(lines, "父线程：不可用或不在受信任工作空间")
	default:
		lines = append(lines, "父线程：无 · 当前为原生根线程")
	}
	lines = append(lines, fmt.Sprintf("直接子线程：%d 个可见", len(relations.Children)))
	if relations.Truncated > 0 {
		lines = append(lines, fmt.Sprintf("未展示：%d 个直接子线程", relations.Truncated))
	}
	lines = append(lines, "", "关联线程")

	nextCode := 1
	appendNode := func(node thread.ThreadRelationNode, role string) {
		code := fmt.Sprintf("%d", nextCode)
		nextCode++
		label := strings.Join([]string{
			role,
			threadRelationTitle(node),
			threadRelationField(node.WorkspaceName),
			formatThreadStatus(node.Status),
		}, " ｜ ")
		lines = append(lines, code+"  "+label)
		options = append(options, controlOption{
			Code: code, Label: label, Action: actionCodexUseGlobalThread,
			Value: node.ID, Query: node.WorkspaceID,
		})
		viewNode := visual.ThreadMapNode{
			Code: code, Title: threadRelationTitle(node), Workspace: threadRelationField(node.WorkspaceName),
			Status: formatThreadStatus(node.Status),
		}
		if role == "父线程" {
			viewNode.Role = visual.ThreadMapParent
			threadMap.Parent = &viewNode
		} else {
			viewNode.Role = visual.ThreadMapChild
			threadMap.Children = append(threadMap.Children, viewNode)
		}
	}
	if relations.Parent != nil {
		appendNode(*relations.Parent, "父线程")
	}
	for _, child := range relations.Children {
		appendNode(child, "直接子线程")
	}
	if relations.Parent == nil && len(relations.Children) == 0 {
		lines = append(lines, "当前目标没有可接管的一层关系节点。")
	}

	actions := []controlOption{
		{Code: "8", Label: "刷新关系图", Action: actionThreadRelations},
		{Code: "9", Label: "全部线程", Action: actionCodexGlobalThreadPage, Page: 1},
	}
	options = append(options, actions...)
	lines = append(lines, "", "快捷操作", renderControlOptions(actions))
	if !h.storeChoice(userID, viewSessionRelations, options, actionCurrentSession) {
		return textControlPage(controlStateFailureResult().Text)
	}
	text := strings.Join(lines, "\n") + "\n\n回复关系节点编号即可接管；图中只读取一层原生关系，10 分钟内有效，0 返回当前线程。"
	return controlPage{Text: text, visual: &actionVisual{ThreadMap: threadMap}}
}

func threadRelationField(value string) string {
	value = strings.ReplaceAll(strings.TrimSpace(value), " ｜ ", " ")
	return normalizeSessionLine(value, 46)
}

// threadRelationTitle 不使用 Preview 回退，避免把对话摘要带入关系图。
func threadRelationTitle(node thread.ThreadRelationNode) string {
	if name := strings.TrimSpace(node.Title); name != "" {
		return threadRelationField(name)
	}
	return "未命名线程 · " + thread.ShortCode(node.ID)
}

func threadBelongsToWorkspace(cwd string, definition workspace.Definition) bool {
	root, rootErr := filepath.EvalSymlinks(definition.Root)
	threadRoot, threadErr := filepath.EvalSymlinks(strings.TrimSpace(cwd))
	if rootErr != nil || threadErr != nil {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(threadRoot))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func (h *Handler) openCodexAccount(ctx context.Context, userID string) string {
	global, ok := h.codex.(codex.GlobalControlClient)
	if !ok {
		return "Codex 账号控制面当前不可用。"
	}
	account, err := global.ReadAccount(ctx)
	if err != nil {
		return "读取 Codex 账号状态失败：" + err.Error()
	}
	lines := []string{"Codex 账号与额度", ""}
	if account.Type == "" {
		lines = append(lines, "账号：未登录")
	} else {
		lines = append(lines, "认证方式："+displayAccountType(account.Type))
		if account.PlanType != "" {
			lines = append(lines, "计划："+account.PlanType)
		}
		if account.Email != "" {
			lines = append(lines, "账号："+maskAccountEmail(account.Email))
		}
	}
	lines = append(lines, "需要 OpenAI 登录："+yesNoText(account.RequiresOpenAIAuth))
	if usage, usageOK := h.codex.(codex.UsageProvider); usageOK {
		if limits, exists := usage.RateLimits(); exists {
			if limits.Primary != nil {
				lines = append(lines, formatRateLimit("主额度", limits.Primary))
			}
			if limits.Secondary != nil {
				lines = append(lines, formatRateLimit("次额度", limits.Secondary))
			}
		}
	}
	options := []controlOption{
		{Label: "线程用量", Action: actionCodexUsage},
		{Label: "模型与权限", Action: actionCodexModelOverview},
		{Label: "返回全局总览", Action: actionCodexGlobalOverview},
	}
	lines = append(lines, "", renderControlOptions(options))
	if !h.storeChoice(userID, viewCodexAccount, options, actionCodexGlobalOverview) {
		return controlStateFailureResult().Text
	}
	return strings.Join(lines, "\n") + "\n\n微信端只读，不提供退出登录或凭据修改。"
}

func (h *Handler) openCodexModelOverview(ctx context.Context, userID string) string {
	capabilities, ok := h.codex.(codex.CapabilityClient)
	if !ok {
		return "Codex 模型目录当前不可用。"
	}
	models, err := capabilities.ListModels(ctx)
	if err != nil {
		return "读取 Codex 模型目录失败：" + err.Error()
	}
	defaultModel := "未标记"
	for _, model := range models {
		if model.IsDefault {
			defaultModel = modelLabel(model)
			break
		}
	}
	target := "未选择"
	if threadClient, currentErr := h.sessionContext(); currentErr == nil {
		if current, readErr := h.sessions.Current(ctx, userID, threadClient); readErr == nil {
			target = threadTitle(current.Info)
		}
	}
	lines := []string{
		"Codex 模型与权限", "",
		fmt.Sprintf("可用模型：%d 个", len(models)),
		"默认模型：" + defaultModel,
		"审批策略：never",
		"沙箱：danger-full-access",
		"目标线程：" + target,
	}
	options := []controlOption{
		{Label: "设置目标线程模型", Action: actionThreadModels},
		{Label: "查看执行权限", Action: actionCodexPermissions},
		{Label: "返回全局总览", Action: actionCodexGlobalOverview},
	}
	lines = append(lines, "", renderControlOptions(options))
	if !h.storeChoice(userID, viewCodexModelOverview, options, actionCodexGlobalOverview) {
		return controlStateFailureResult().Text
	}
	return strings.Join(lines, "\n") + "\n\n模型选择只作用于当前目标线程。"
}

func displayAccountType(accountType string) string {
	switch accountType {
	case "chatgpt":
		return "ChatGPT"
	case "apiKey", "apikey":
		return "API Key"
	case "amazonBedrock":
		return "Amazon Bedrock"
	default:
		return accountType
	}
}

func maskAccountEmail(email string) string {
	parts := strings.Split(strings.TrimSpace(email), "@")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "已登录"
	}
	return string([]rune(parts[0])[0]) + "***@" + parts[1]
}

func yesNoText(value bool) string {
	if value {
		return "是"
	}
	return "否"
}
