package messaging

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
)

type controlVisualRenderer interface {
	Render(context.Context, visual.Card) (*visual.Artifact, error)
}

type controlDirectoryRenderer interface {
	RenderDirectory(context.Context, visual.Directory) (*visual.Artifact, error)
}

type controlWorkbenchRenderer interface {
	RenderWorkbench(context.Context, visual.Workbench) (*visual.Artifact, error)
}

type controlReviewRenderer interface {
	RenderReview(context.Context, visual.Review) (*visual.Artifact, error)
}

var controlOptionPattern = regexp.MustCompile(`^([1-9][0-9]?)\s{2,}(.+)$`)
var controlDirectorySectionPattern = regexp.MustCompile(`^\[([1-4])\]\s{2,}(.+)$`)

// sendControlReply 优先发送视觉卡片；任何渲染或图片上传错误都会回退为完整文字。
func (h *Handler) sendControlReply(ctx context.Context, client *ilink.Client, userID, reply, contextToken, clientID string) error {
	if h.visual == nil {
		return SendTextReply(ctx, client, userID, reply, contextToken, clientID)
	}

	workbench, isWorkbench := controlWorkbenchFromText(reply)
	directory, isDirectory := controlDirectoryFromText(reply)
	review, isReview := reviewControlFromText(reply)
	var artifact *visual.Artifact
	var err error
	if renderer, supportsWorkbench := h.visual.(controlWorkbenchRenderer); isWorkbench && supportsWorkbench {
		workbench.Style = h.currentVisualStyle(userID)
		artifact, err = renderer.RenderWorkbench(ctx, workbench)
	} else if renderer, supportsReview := h.visual.(controlReviewRenderer); isReview && supportsReview {
		review.Style = h.currentVisualStyle(userID)
		artifact, err = renderer.RenderReview(ctx, review)
	} else if renderer, supportsDirectory := h.visual.(controlDirectoryRenderer); isDirectory && supportsDirectory {
		directory.Style = h.currentVisualStyle(userID)
		artifact, err = renderer.RenderDirectory(ctx, directory)
	} else {
		card := controlCardFromText(reply)
		card.Style = h.currentVisualStyle(userID)
		artifact, err = h.visual.Render(ctx, card)
	}
	if err != nil {
		log.Printf("[visual] render failed for %s, falling back to text: %v", ilink.LogLabel(userID), err)
		return SendTextReply(ctx, client, userID, reply, contextToken, clientID)
	}
	if artifact == nil || strings.TrimSpace(artifact.Path) == "" {
		log.Printf("[visual] renderer returned an empty artifact for %s, falling back to text", ilink.LogLabel(userID))
		return SendTextReply(ctx, client, userID, reply, contextToken, clientID)
	}
	if artifact.Cleanup != nil {
		defer artifact.Cleanup()
	}

	if err := SendMediaFromPath(ctx, client, userID, artifact.Path, contextToken); err != nil {
		log.Printf("[visual] image delivery failed for %s, falling back to text: %v", ilink.LogLabel(userID), err)
		return SendTextReply(ctx, client, userID, reply, contextToken, clientID)
	}
	if isWorkbench || isDirectory {
		return nil
	}
	card := controlCardFromText(reply)
	if caption := controlCaption(reply, card); caption != "" {
		if err := SendTextReply(ctx, client, userID, caption, contextToken, clientID); err != nil {
			return fmt.Errorf("send visual card caption: %w", err)
		}
	}
	return nil
}

func controlWorkbenchFromText(reply string) (visual.Workbench, bool) {
	lines := nonEmptyControlLines(strings.TrimSpace(strings.ReplaceAll(reply, "\r\n", "\n")))
	if len(lines) == 0 || lines[0] != "Codex 全局工作台" {
		return visual.Workbench{}, false
	}
	workbench := visual.Workbench{
		Title: "全局工作台", Subtitle: "从微信统筹 Codex 桌面端、CLI 与远程执行",
		Commands: workbenchCodexCommandGroups(),
		Footer:   "回复编号操作 · 斜杠命令可直接发送 · 普通内容进入当前目标 · 首页 5 分钟内有效",
	}
	section := "facts"
	quickIcons := map[string]string{
		"5": "messages-square", "6": "plus", "7": "list-filter", "8": "folder-kanban", "9": "refresh-cw",
	}
	for _, line := range lines[1:] {
		switch line {
		case "最近线程":
			section = "threads"
			continue
		case "快捷操作":
			section = "actions"
			continue
		case "Codex 功能":
			section = "commands"
			continue
		}
		if strings.HasPrefix(line, "从微信统筹") {
			workbench.Subtitle = line
			continue
		}
		if strings.HasPrefix(line, "当前目标：") {
			fields := splitWorkbenchFields(strings.TrimPrefix(line, "当前目标："), 4)
			if len(fields) == 4 {
				workbench.Target = visual.WorkbenchTarget{
					Title: fields[0], Workspace: fields[1], Status: fields[2], Time: fields[3],
					Available: fields[0] != "尚未选择",
				}
			}
			continue
		}
		if section == "threads" {
			if matches := controlOptionPattern.FindStringSubmatch(line); len(matches) == 3 {
				fields := splitWorkbenchFields(matches[2], 5)
				if len(fields) == 5 {
					workbench.Threads = append(workbench.Threads, visual.WorkbenchThread{
						Code: matches[1], Title: fields[0], Workspace: fields[1], Status: fields[2], Time: fields[3],
						Current: strings.Contains(fields[4], "当前目标"), Wechat: workbenchWechatBadge(fields[4]),
					})
				}
			}
			continue
		}
		if section == "actions" {
			if matches := controlOptionPattern.FindStringSubmatch(line); len(matches) == 3 {
				label, meta := splitDirectoryLabel(strings.TrimSpace(matches[2]))
				workbench.Actions = append(workbench.Actions, visual.WorkbenchAction{
					Code: matches[1], Label: label, Meta: meta, Icon: quickIcons[matches[1]],
				})
			}
			continue
		}
		if section == "commands" {
			continue
		}
		if label, value, ok := controlFact(line); ok {
			switch label {
			case "全局状态":
				workbench.State = value
			case "工作空间", "全部线程", "运行中", "微信队列":
				workbench.Facts = append(workbench.Facts, visual.Fact{Label: label, Value: value})
			}
		}
	}
	return workbench, workbench.Target.Title != "" && len(workbench.Actions) == 5
}

func workbenchCodexCommandGroups() []visual.WorkbenchCommandGroup {
	commandGroups := codexSlashWorkbenchGroups()
	groups := make([]visual.WorkbenchCommandGroup, 0, len(commandGroups))
	for _, commandGroup := range commandGroups {
		group := visual.WorkbenchCommandGroup{Title: commandGroup.Title}
		for _, command := range commandGroup.Commands {
			tone := "native"
			if command.Support == codexSlashAdapted {
				tone = "adapted"
			}
			group.Commands = append(group.Commands, visual.WorkbenchCommand{
				Label: command.Label, Command: "/" + command.Name, Tone: tone,
			})
		}
		groups = append(groups, group)
	}
	return groups
}

func splitWorkbenchFields(line string, expected int) []string {
	parts := strings.Split(strings.TrimSpace(line), " ｜ ")
	if len(parts) != expected {
		return nil
	}
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
		if parts[index] == "" {
			return nil
		}
	}
	return parts
}

func workbenchWechatBadge(badge string) string {
	for _, candidate := range []string{"微信执行中", "微信发送中", "微信等待中"} {
		if strings.Contains(badge, candidate) {
			return candidate
		}
	}
	return ""
}

func reviewControlFromText(reply string) (visual.Review, bool) {
	lines := nonEmptyControlLines(strings.TrimSpace(strings.ReplaceAll(reply, "\r\n", "\n")))
	if len(lines) == 0 || lines[0] != "Codex 移动审查" {
		return visual.Review{}, false
	}
	review := visual.Review{Footer: "回复数字继续；回复“文字版”获取完整审查原文；0 返回 Codex 开发"}
	inFindings := false
	var current *visual.ReviewFinding
	flushFinding := func() {
		if current == nil {
			return
		}
		review.Findings = append(review.Findings, *current)
		current = nil
	}
	for _, line := range lines[1:] {
		if matches := controlOptionPattern.FindStringSubmatch(line); len(matches) == 3 {
			flushFinding()
			review.Options = append(review.Options, visual.Option{Number: matches[1], Label: strings.TrimSpace(matches[2])})
			continue
		}
		if isControlInstruction(line) {
			review.Footer = strings.TrimSuffix(line, "。")
			continue
		}
		if line == "重点问题" {
			inFindings = true
			continue
		}
		if inFindings {
			if len(line) >= 5 && line[0] == '[' && line[3] == ']' && line[1] == 'P' {
				flushFinding()
				current = &visual.ReviewFinding{Priority: line[1:3], Title: strings.TrimSpace(line[4:])}
				continue
			}
			if current != nil {
				if strings.HasPrefix(line, "位置：") {
					current.Location = strings.TrimSpace(strings.TrimPrefix(line, "位置："))
				} else if current.Detail == "" {
					current.Detail = line
				}
				continue
			}
		}
		switch {
		case strings.HasPrefix(line, "结论："):
			review.Headline = strings.TrimSpace(strings.TrimPrefix(line, "结论："))
		case strings.HasPrefix(line, "工作空间："):
			review.Workspace = strings.TrimSpace(strings.TrimPrefix(line, "工作空间："))
		case strings.HasPrefix(line, "目标线程："):
			review.Thread = strings.TrimSpace(strings.TrimPrefix(line, "目标线程："))
		case strings.HasPrefix(line, "审查范围："):
			review.Target = strings.TrimSpace(strings.TrimPrefix(line, "审查范围："))
		case strings.HasPrefix(line, "审查状态："):
			review.Verdict = visual.ReviewVerdict(strings.TrimSpace(strings.TrimPrefix(line, "审查状态：")))
		case strings.HasPrefix(line, "最高优先级："):
			review.Highest = strings.TrimSpace(strings.TrimPrefix(line, "最高优先级："))
		case strings.HasPrefix(line, "摘要："):
			review.Summary = strings.TrimSpace(strings.TrimPrefix(line, "摘要："))
		case strings.HasPrefix(line, "变更事实："):
			review.Facts = append(review.Facts, visual.Fact{Label: "变更", Value: strings.TrimSpace(strings.TrimPrefix(line, "变更事实："))})
		case strings.HasPrefix(line, "验证事实："):
			review.Facts = append(review.Facts, visual.Fact{Label: "验证", Value: strings.TrimSpace(strings.TrimPrefix(line, "验证事实："))})
		case strings.HasPrefix(line, "交付事实："):
			review.Facts = append(review.Facts, visual.Fact{Label: "交付", Value: strings.TrimSpace(strings.TrimPrefix(line, "交付事实："))})
		}
	}
	flushFinding()
	return review, review.Headline != "" && review.Workspace != "" && review.Thread != "" && review.Target != "" && len(review.Options) > 0
}

func controlDirectoryFromText(reply string) (visual.Directory, bool) {
	lines := nonEmptyControlLines(strings.TrimSpace(strings.ReplaceAll(reply, "\r\n", "\n")))
	if len(lines) == 0 || lines[0] != "Codex 全部功能" {
		return visual.Directory{}, false
	}
	directory := visual.Directory{
		Title: "Codex 全部功能", Subtitle: "按领域浏览 Codex 与 codex-link-clawbot 控制能力",
		Footer: "回复编号或直接发送 /command · 0 返回全局工作台 · 目录 30 分钟内有效",
	}
	icons := map[string]string{
		"1": "activity", "2": "folder-kanban", "3": "list-todo", "4": "settings-2",
	}
	sectionIndex := -1
	for _, line := range lines[1:] {
		if matches := controlDirectorySectionPattern.FindStringSubmatch(line); len(matches) == 3 {
			directory.Sections = append(directory.Sections, visual.DirectorySection{
				Code: matches[1], Title: strings.TrimSpace(matches[2]), Icon: icons[matches[1]],
			})
			sectionIndex = len(directory.Sections) - 1
			continue
		}
		if matches := controlOptionPattern.FindStringSubmatch(line); len(matches) == 3 && sectionIndex >= 0 {
			label, meta := splitDirectoryLabel(strings.TrimSpace(matches[2]))
			directory.Sections[sectionIndex].Items = append(directory.Sections[sectionIndex].Items, visual.DirectoryItem{
				Code: matches[1], Label: label, Meta: meta,
			})
			continue
		}
		if sectionIndex < 0 {
			if line == "按领域浏览 Codex 与 codex-link-clawbot 控制能力" {
				directory.Subtitle = line
				continue
			}
			if label, value, ok := controlFact(line); ok {
				directory.Facts = append(directory.Facts, visual.Fact{Label: label, Value: value})
			}
		}
	}
	return directory, len(directory.Sections) == 4
}

func splitDirectoryLabel(label string) (string, string) {
	parts := strings.SplitN(label, " · ", 2)
	if len(parts) == 1 {
		return strings.TrimSpace(label), ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func (h *Handler) currentVisualStyle(userID string) visual.Style {
	if h.preferences == nil {
		return visual.DefaultStyle
	}
	return h.preferences.Get(userID).Style
}

// controlCardFromText 将内部控制文本收敛为固定结构，模板层会再次执行 HTML 转义。
func controlCardFromText(reply string) visual.Card {
	original := strings.TrimSpace(strings.ReplaceAll(reply, "\r\n", "\n"))
	lines := nonEmptyControlLines(original)
	variant := controlCardVariant(original)
	card := visual.Card{
		Variant: variant,
		Title:   "codex-link-clawbot",
		Footer:  "发送 / 可随时打开操作菜单",
	}
	if len(lines) == 0 {
		return card
	}

	first := lines[0]
	consumeFirst := true
	switch {
	case first == "codex-link-clawbot":
		card.Title = first
	case first == "Codex 移动审查":
		card.Title = first
	case first == "codex-link-clawbot 运行状态", first == "Codex 工作空间", first == "Codex 全局技能与工具",
		first == "最近结果与交付箱", first == "呈现与安全", first == "codex-link-clawbot 有效配置", first == "codex-link-clawbot 诊断中心",
		first == "交付箱", first == "交付详情",
		first == "codex-link-clawbot 请求队列", first == "codex-link-clawbot 执行记录":
		card.Title = first
	case first == "视觉风格", first == "视觉风格已切换":
		card.Title = first
	case first == "codex-link-clawbot 回复呈现", first == "codex-link-clawbot 回复方式已切换":
		card.Title = first
	case first == "Codex 线程", first == "Codex 开发", first == "Codex 全局总览", first == "Codex 全部线程", first == "Codex 运行中线程",
		first == "Codex 已归档线程", first == "Codex 全局搜索", first == "Codex 账号与额度", first == "Codex 模型与权限",
		first == "当前线程", first == "线程详情", first == "归档线程详情", first == "搜索线程",
		first == "新建线程", first == "重命名线程", first == "线程模型", first == "推理强度", first == "设置线程目标",
		first == "Codex 可用命令", first == "Codex 执行权限 · /permissions", first == "Codex 用量 · /usage", first == "Codex 线程目标 · /goal":
		card.Title = first
	case strings.HasPrefix(first, "Codex 命令 · "):
		card.Title = first
	case strings.HasPrefix(first, "选择线程") || strings.HasPrefix(first, "恢复线程"):
		card.Title = first
	case strings.HasPrefix(first, "准备归档线程："):
		card.Title = "归档确认"
		card.Subtitle = strings.TrimSpace(strings.TrimPrefix(first, "准备归档线程："))
	case strings.HasPrefix(first, "codex-link-clawbot 执行状态："):
		card.Title = "codex-link-clawbot 执行状态"
		card.Subtitle = strings.TrimSpace(strings.TrimPrefix(first, "codex-link-clawbot 执行状态："))
	case strings.HasPrefix(first, "Codex："):
		card.Title = "Codex 运行信息"
		card.Subtitle = strings.TrimSpace(strings.TrimPrefix(first, "Codex："))
	case strings.HasPrefix(first, "直接发送文字、图片或文件"):
		card.Title = "使用说明"
		consumeFirst = false
	default:
		title, remainder := splitControlLead(first)
		if utf8.RuneCountInString(title) <= 32 {
			card.Title = title
			if remainder != "" {
				card.Body = append(card.Body, remainder)
			}
		} else {
			card.Title = controlFallbackTitle(variant)
			consumeFirst = false
		}
	}

	start := 0
	if consumeFirst {
		start = 1
	}
	for _, line := range lines[start:] {
		if matches := controlOptionPattern.FindStringSubmatch(line); len(matches) == 3 {
			card.Options = append(card.Options, visual.Option{Number: matches[1], Label: strings.TrimSpace(matches[2])})
			continue
		}
		if isControlInstruction(line) {
			card.Footer = line
			continue
		}
		if label, value, ok := controlFact(line); ok {
			card.Facts = append(card.Facts, visual.Fact{Label: label, Value: value})
			continue
		}
		card.Body = append(card.Body, line)
	}
	return card
}

func nonEmptyControlLines(text string) []string {
	raw := strings.Split(text, "\n")
	lines := make([]string, 0, len(raw))
	for _, line := range raw {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func controlCardVariant(text string) visual.Variant {
	switch {
	case text == "codex-link-clawbot" || strings.HasPrefix(text, "codex-link-clawbot\n"):
		return visual.VariantHome
	case strings.HasPrefix(text, "准备归档") || strings.HasPrefix(text, "准备取消") || strings.HasPrefix(text, "准备清空"):
		return visual.VariantWarning
	case strings.HasPrefix(text, "codex-link-clawbot 运行状态") || strings.HasPrefix(text, "最近结果与交付箱") ||
		strings.HasPrefix(text, "呈现与安全") || strings.HasPrefix(text, "codex-link-clawbot 有效配置") ||
		strings.HasPrefix(text, "codex-link-clawbot 诊断中心") || strings.HasPrefix(text, "Codex 工作空间") ||
		strings.HasPrefix(text, "Codex 全局技能与工具") ||
		strings.HasPrefix(text, "交付箱") || strings.HasPrefix(text, "交付详情"):
		return visual.VariantSystem
	case strings.HasPrefix(text, "视觉风格已切换"):
		return visual.VariantSuccess
	case strings.HasPrefix(text, "视觉风格"):
		return visual.VariantSystem
	case strings.HasPrefix(text, "codex-link-clawbot 回复方式已切换"):
		return visual.VariantSuccess
	case strings.HasPrefix(text, "codex-link-clawbot 回复呈现"):
		return visual.VariantSystem
	case strings.HasPrefix(text, "codex-link-clawbot 请求队列") || strings.HasPrefix(text, "codex-link-clawbot 执行记录") || strings.HasPrefix(text, "codex-link-clawbot 执行状态"):
		return visual.VariantProgress
	case strings.HasPrefix(text, "Codex 全局") || strings.HasPrefix(text, "Codex 全部线程") || strings.HasPrefix(text, "Codex 运行中线程") ||
		strings.HasPrefix(text, "Codex 已归档线程") || strings.HasPrefix(text, "Codex 账号与额度") || strings.HasPrefix(text, "Codex 模型与权限") ||
		strings.HasPrefix(text, "Codex 线程") || strings.HasPrefix(text, "Codex 开发") || strings.HasPrefix(text, "Codex 可用命令") ||
		strings.HasPrefix(text, "Codex 命令 · ") || strings.HasPrefix(text, "Codex 执行权限 · /permissions") ||
		strings.HasPrefix(text, "Codex 用量 · /usage") || strings.HasPrefix(text, "当前线程\n") ||
		strings.HasPrefix(text, "线程列表") || strings.HasPrefix(text, "线程详情") ||
		strings.HasPrefix(text, "归档线程详情") || strings.HasPrefix(text, "选择线程") ||
		strings.HasPrefix(text, "恢复线程") || strings.HasPrefix(text, "搜索线程") ||
		strings.HasPrefix(text, "线程模型") || strings.HasPrefix(text, "推理强度"):
		return visual.VariantSession
	case strings.HasPrefix(text, "已") || strings.Contains(text, "已切换") || strings.Contains(text, "已恢复") ||
		strings.Contains(text, "已重命名") || strings.Contains(text, "已归档"):
		return visual.VariantSuccess
	case strings.Contains(text, "失败") || strings.Contains(text, "不可用") || strings.Contains(text, "不存在") ||
		strings.Contains(text, "无法读取") || strings.Contains(text, "必须") || strings.Contains(text, "失效") ||
		strings.Contains(text, "暂时不能") || strings.Contains(text, "没有找到") || strings.Contains(text, "当前没有") ||
		strings.Contains(text, "还没有") || strings.Contains(text, "本条消息未交给"):
		return visual.VariantWarning
	case strings.HasPrefix(text, "codex-link-clawbot 执行状态") || strings.Contains(text, "已运行：") || strings.Contains(text, "进度"):
		return visual.VariantProgress
	case strings.Contains(text, "线程"):
		return visual.VariantSession
	case strings.HasPrefix(text, "Codex：") || strings.Contains(text, "Codex 工作目录") || strings.Contains(text, "协议："):
		return visual.VariantSystem
	case strings.HasPrefix(text, "codex-link-clawbot"):
		return visual.VariantHome
	default:
		return visual.VariantNeutral
	}
}

func controlFallbackTitle(variant visual.Variant) string {
	switch variant {
	case visual.VariantWarning:
		return "需要处理"
	case visual.VariantSuccess:
		return "操作完成"
	case visual.VariantProgress:
		return "执行状态"
	default:
		return "codex-link-clawbot"
	}
}

func splitControlLead(line string) (string, string) {
	line = strings.TrimSpace(line)
	for _, separator := range []string{"。", "；"} {
		if index := strings.Index(line, separator); index >= 0 {
			title := strings.TrimSpace(line[:index])
			remainder := strings.TrimSpace(line[index+len(separator):])
			return title, remainder
		}
	}
	return strings.TrimRight(line, "。；"), ""
}

func controlFact(line string) (string, string, bool) {
	separator := "："
	index := strings.Index(line, separator)
	if index < 0 {
		separator = ":"
		index = strings.Index(line, separator)
	}
	if index <= 0 {
		return "", "", false
	}
	label := strings.TrimSpace(line[:index])
	value := strings.TrimSpace(line[index+len(separator):])
	if value == "" || utf8.RuneCountInString(label) > 24 || strings.ContainsAny(label, "。；，,!?！？") {
		return "", "", false
	}
	return label, value, true
}

func isControlInstruction(line string) bool {
	return strings.HasPrefix(line, "回复") || strings.HasPrefix(line, "发送新的") ||
		strings.HasPrefix(line, "发送线程名称") || strings.HasPrefix(line, "请输入") ||
		strings.Contains(line, "发送“状态”")
}

func controlCaption(reply string, card visual.Card) string {
	lines := nonEmptyControlLines(reply)
	for index := len(lines) - 1; index >= 0; index-- {
		if isControlInstruction(lines[index]) {
			return lines[index]
		}
	}
	if len(card.Options) > 0 {
		return "回复数字选择，回复 0 返回。"
	}
	return ""
}
