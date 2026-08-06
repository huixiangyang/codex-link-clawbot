package messaging

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/huixiangyang/weclaw/internal/ilink"
	"github.com/huixiangyang/weclaw/internal/visual"
)

type controlVisualRenderer interface {
	Render(context.Context, visual.Card) (*visual.Artifact, error)
}

type controlDirectoryRenderer interface {
	RenderDirectory(context.Context, visual.Directory) (*visual.Artifact, error)
}

var controlOptionPattern = regexp.MustCompile(`^([1-9][0-9]?)\s{2,}(.+)$`)
var controlDirectorySectionPattern = regexp.MustCompile(`^\[([1-6])\]\s{2,}(.+)$`)

// sendControlReply 优先发送视觉卡片；任何渲染或图片上传错误都会回退为完整文字。
func (h *Handler) sendControlReply(ctx context.Context, client *ilink.Client, userID, reply, contextToken, clientID string) error {
	if h.visual == nil {
		return SendTextReply(ctx, client, userID, reply, contextToken, clientID)
	}

	directory, isDirectory := controlDirectoryFromText(reply)
	var artifact *visual.Artifact
	var err error
	if renderer, supportsDirectory := h.visual.(controlDirectoryRenderer); isDirectory && supportsDirectory {
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
	if isDirectory {
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

func controlDirectoryFromText(reply string) (visual.Directory, bool) {
	lines := nonEmptyControlLines(strings.TrimSpace(strings.ReplaceAll(reply, "\r\n", "\n")))
	if len(lines) == 0 || lines[0] != "WeClaw 操作总览" {
		return visual.Directory{}, false
	}
	directory := visual.Directory{
		Title: "操作总览", Subtitle: "稳定编号，一步直达",
		Footer: "回复编号直接操作 · 0 退出 · 总览 30 分钟内有效",
	}
	icons := map[string]string{
		"1": "messages-square", "2": "list-todo", "3": "palette",
		"4": "package-open", "5": "settings-2", "6": "activity",
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
			if strings.HasPrefix(line, "能力边界：") {
				directory.Subtitle = strings.TrimSpace(strings.TrimPrefix(line, "能力边界："))
				continue
			}
			if label, value, ok := controlFact(line); ok {
				directory.Facts = append(directory.Facts, visual.Fact{Label: label, Value: value})
			}
		}
	}
	return directory, len(directory.Sections) == 6
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
		Title:   "WeClaw",
		Footer:  "发送 / 可随时打开操作菜单",
	}
	if len(lines) == 0 {
		return card
	}

	first := lines[0]
	consumeFirst := true
	switch {
	case first == "WeClaw":
		card.Title = first
	case first == "WeClaw 运行与安全", first == "WeClaw 项目入口", first == "Codex 能力",
		first == "WeClaw 功能中心", first == "WeClaw 设置中心", first == "WeClaw 有效配置", first == "WeClaw 诊断中心", first == "自动化中心",
		first == "素材与交付", first == "链接素材", first == "交付记录", first == "提示词模板",
		first == "WeClaw 请求队列", first == "WeClaw 执行记录":
		card.Title = first
	case first == "视觉风格", first == "视觉风格已切换":
		card.Title = first
	case first == "WeClaw 回复呈现", first == "WeClaw 回复方式已切换":
		card.Title = first
	case first == "Codex 线程", first == "当前线程", first == "线程详情", first == "归档线程详情", first == "搜索线程",
		first == "新建线程", first == "重命名线程", first == "线程模型", first == "推理强度", first == "设置线程目标":
		card.Title = first
	case strings.HasPrefix(first, "选择线程") || strings.HasPrefix(first, "恢复线程"):
		card.Title = first
	case strings.HasPrefix(first, "准备归档线程："):
		card.Title = "归档确认"
		card.Subtitle = strings.TrimSpace(strings.TrimPrefix(first, "准备归档线程："))
	case strings.HasPrefix(first, "WeClaw 执行状态："):
		card.Title = "WeClaw 执行状态"
		card.Subtitle = strings.TrimSpace(strings.TrimPrefix(first, "WeClaw 执行状态："))
	case strings.HasPrefix(first, "Codex："):
		card.Title = "Codex 运行信息"
		card.Subtitle = strings.TrimSpace(strings.TrimPrefix(first, "Codex："))
	case strings.HasPrefix(first, "自动化详情："):
		card.Title = "自动化详情"
		card.Subtitle = strings.TrimSpace(strings.TrimPrefix(first, "自动化详情："))
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
	case text == "WeClaw" || strings.HasPrefix(text, "WeClaw\n"):
		return visual.VariantHome
	case strings.HasPrefix(text, "准备归档") || strings.HasPrefix(text, "准备取消") || strings.HasPrefix(text, "准备清空"):
		return visual.VariantWarning
	case strings.HasPrefix(text, "WeClaw 运行与安全") || strings.HasPrefix(text, "WeClaw 功能中心") ||
		strings.HasPrefix(text, "WeClaw 设置中心") || strings.HasPrefix(text, "WeClaw 有效配置") ||
		strings.HasPrefix(text, "WeClaw 诊断中心") || strings.HasPrefix(text, "WeClaw 项目入口") ||
		strings.HasPrefix(text, "Codex 能力") ||
		strings.HasPrefix(text, "自动化") || strings.HasPrefix(text, "素材与交付"):
		return visual.VariantSystem
	case strings.HasPrefix(text, "视觉风格已切换"):
		return visual.VariantSuccess
	case strings.HasPrefix(text, "视觉风格"):
		return visual.VariantSystem
	case strings.HasPrefix(text, "WeClaw 回复方式已切换"):
		return visual.VariantSuccess
	case strings.HasPrefix(text, "WeClaw 回复呈现"):
		return visual.VariantSystem
	case strings.HasPrefix(text, "WeClaw 请求队列") || strings.HasPrefix(text, "WeClaw 执行记录") || strings.HasPrefix(text, "WeClaw 执行状态"):
		return visual.VariantProgress
	case strings.HasPrefix(text, "Codex 线程") || strings.HasPrefix(text, "当前线程\n") ||
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
	case strings.HasPrefix(text, "WeClaw 执行状态") || strings.Contains(text, "已运行：") || strings.Contains(text, "进度"):
		return visual.VariantProgress
	case strings.Contains(text, "线程"):
		return visual.VariantSession
	case strings.HasPrefix(text, "Codex：") || strings.Contains(text, "Codex 工作目录") || strings.Contains(text, "协议：") ||
		strings.Contains(text, "自动化详情"):
		return visual.VariantSystem
	case strings.HasPrefix(text, "WeClaw"):
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
		return "WeClaw"
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
	if value == "" || utf8.RuneCountInString(label) > 12 || strings.ContainsAny(label, "。；，,!?！？") {
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
