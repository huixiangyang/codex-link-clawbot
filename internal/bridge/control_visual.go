package bridge

import (
	"context"
	"fmt"
	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
	"log"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/huixiangyang/codex-link-clawbot/internal/ilink"
	"github.com/huixiangyang/codex-link-clawbot/internal/visual"
)

type VisualRenderer interface {
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

type controlThreadMapRenderer interface {
	RenderThreadMap(context.Context, visual.ThreadMap) (*visual.Artifact, error)
}

var controlOptionPattern = regexp.MustCompile(`^([1-9][0-9]?)\s{2,}(.+)$`)

// sendControlReply 优先发送视觉卡片；任何渲染或图片上传错误都会回退为完整文字。
func (h *Handler) sendControlReply(ctx context.Context, client *ilink.Client, userID, reply, contextToken, clientID string) error {
	return h.sendControlVisualReply(ctx, client, userID, reply, contextToken, clientID, nil)
}

// sendControlVisualReply 只消费控制器给出的专用视图，不再从展示文本反向推断页面类型。
func (h *Handler) sendControlVisualReply(ctx context.Context, client *ilink.Client, userID, reply, contextToken, clientID string, view *actionVisual) error {
	if h.visual == nil {
		return SendTextReply(ctx, client, userID, reply, contextToken, clientID)
	}

	var artifact *visual.Artifact
	var err error
	dedicated := false
	style := h.currentVisualStyle(userID)
	switch {
	case view != nil && view.Workbench != nil:
		if renderer, ok := h.visual.(controlWorkbenchRenderer); ok {
			model := *view.Workbench
			model.Style = style
			artifact, err = renderer.RenderWorkbench(ctx, model)
			dedicated = true
		}
	case view != nil && view.ThreadMap != nil:
		if renderer, ok := h.visual.(controlThreadMapRenderer); ok {
			model := *view.ThreadMap
			model.Style = style
			artifact, err = renderer.RenderThreadMap(ctx, model)
			dedicated = true
		}
	case view != nil && view.Review != nil:
		if renderer, ok := h.visual.(controlReviewRenderer); ok {
			model := *view.Review
			model.Style = style
			artifact, err = renderer.RenderReview(ctx, model)
			dedicated = true
		}
	case view != nil && view.Directory != nil:
		if renderer, ok := h.visual.(controlDirectoryRenderer); ok {
			model := *view.Directory
			model.Style = style
			artifact, err = renderer.RenderDirectory(ctx, model)
			dedicated = true
		}
	}
	if !dedicated {
		card := controlCardFromText(reply)
		card.Style = style
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
	if view != nil && (view.Workbench != nil || view.ThreadMap != nil || view.Directory != nil) && dedicated {
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

func workbenchWechatBadge(badge string) string {
	for _, candidate := range []string{"微信执行中", "微信发送中", "微信等待中"} {
		if strings.Contains(badge, candidate) {
			return candidate
		}
	}
	return ""
}

func splitDirectoryLabel(label string) (string, string) {
	parts := strings.SplitN(label, " · ", 2)
	if len(parts) == 1 {
		return strings.TrimSpace(label), ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}

func (h *Handler) currentVisualStyle(userID string) presentation.Style {
	if h.preferences == nil {
		return presentation.DefaultStyle
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
