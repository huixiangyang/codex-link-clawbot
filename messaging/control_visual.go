package messaging

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/huixiangyang/weclaw/ilink"
	"github.com/huixiangyang/weclaw/visual"
)

type controlVisualRenderer interface {
	Render(context.Context, visual.Card) (*visual.Artifact, error)
}

var controlOptionPattern = regexp.MustCompile(`^([1-9][0-9]?)\s{2,}(.+)$`)

// sendControlReply 优先发送视觉卡片；任何渲染或图片上传错误都会回退为完整文字。
func (h *Handler) sendControlReply(ctx context.Context, client *ilink.Client, userID, reply, contextToken, clientID string) error {
	if h.visual == nil {
		return SendTextReply(ctx, client, userID, reply, contextToken, clientID)
	}

	card := controlCardFromText(reply)
	artifact, err := h.visual.Render(ctx, card)
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
	if caption := controlCaption(reply, card); caption != "" {
		if err := SendTextReply(ctx, client, userID, caption, contextToken, clientID); err != nil {
			return fmt.Errorf("send visual card caption: %w", err)
		}
	}
	return nil
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
	case first == "运行中心":
		card.Title = first
	case first == "会话", first == "当前会话", first == "会话详情", first == "归档会话详情", first == "搜索会话",
		first == "工作目录", first == "新建会话", first == "重命名会话":
		card.Title = first
	case strings.HasPrefix(first, "选择会话") || strings.HasPrefix(first, "恢复会话"):
		card.Title = first
	case strings.HasPrefix(first, "准备归档会话："):
		card.Title = "归档确认"
		card.Subtitle = strings.TrimSpace(strings.TrimPrefix(first, "准备归档会话："))
	case strings.HasPrefix(first, "任务状态："):
		card.Title = "任务状态"
		card.Subtitle = strings.TrimSpace(strings.TrimPrefix(first, "任务状态："))
	case strings.HasPrefix(first, "Codex："):
		card.Title = "Codex 运行信息"
		card.Subtitle = strings.TrimSpace(strings.TrimPrefix(first, "Codex："))
	case strings.HasPrefix(first, "巡检详情："):
		card.Title = "巡检详情"
		card.Subtitle = strings.TrimSpace(strings.TrimPrefix(first, "巡检详情："))
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
	case strings.HasPrefix(text, "WeClaw"):
		return visual.VariantHome
	case strings.HasPrefix(text, "准备归档") || strings.HasPrefix(text, "准备取消"):
		return visual.VariantWarning
	case strings.HasPrefix(text, "运行中心"):
		return visual.VariantSystem
	case strings.HasPrefix(text, "任务记录") || strings.HasPrefix(text, "任务详情"):
		return visual.VariantProgress
	case strings.HasPrefix(text, "会话中心") || strings.HasPrefix(text, "当前会话\n") ||
		strings.HasPrefix(text, "会话列表") || strings.HasPrefix(text, "会话详情") ||
		strings.HasPrefix(text, "归档会话详情") || strings.HasPrefix(text, "选择会话") ||
		strings.HasPrefix(text, "恢复会话") || strings.HasPrefix(text, "搜索会话"):
		return visual.VariantSession
	case strings.HasPrefix(text, "已") || strings.Contains(text, "已切换") || strings.Contains(text, "已恢复") ||
		strings.Contains(text, "已重命名") || strings.Contains(text, "已归档"):
		return visual.VariantSuccess
	case strings.Contains(text, "失败") || strings.Contains(text, "不可用") || strings.Contains(text, "不存在") ||
		strings.Contains(text, "无法读取") || strings.Contains(text, "必须") || strings.Contains(text, "失效") ||
		strings.Contains(text, "暂时不能") || strings.Contains(text, "没有找到") || strings.Contains(text, "当前没有") ||
		strings.Contains(text, "还没有") || strings.Contains(text, "本条消息未交给"):
		return visual.VariantWarning
	case strings.HasPrefix(text, "任务状态") || strings.Contains(text, "已运行：") || strings.Contains(text, "进度"):
		return visual.VariantProgress
	case strings.Contains(text, "会话"):
		return visual.VariantSession
	case strings.HasPrefix(text, "Codex：") || strings.HasPrefix(text, "工作目录") || strings.Contains(text, "协议：") ||
		strings.Contains(text, "定时巡检") || strings.Contains(text, "巡检详情"):
		return visual.VariantSystem
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
		return "任务状态"
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
		strings.HasPrefix(line, "发送会话名称") || strings.HasPrefix(line, "请输入") ||
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
