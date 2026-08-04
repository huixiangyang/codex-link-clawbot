package visual

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	documentPageUnits = 32
	documentMaxPages  = 10
)

type DocumentBlock struct {
	Kind     string
	Text     string
	Prefix   string
	Language string
}

type Document struct {
	Theme           Theme
	TimeLabel       string
	ThemeLabel      string
	Kicker          string
	Title           string
	Subtitle        string
	Blocks          []DocumentBlock
	PageNumber      int
	TotalPages      int
	ProgressPercent int
	Footer          string
	Height          int
}

var orderedListPattern = regexp.MustCompile(`^([0-9]{1,3})[.)、]\s+(.+)$`)
var inlineImagePattern = regexp.MustCompile(`!\[([^]]*)\]\([^)]+\)`)
var inlineLinkPattern = regexp.MustCompile(`\[([^]]+)\]\([^)]+\)`)

// PaginateMarkdown 只解析展示所需的 Markdown 子集，不生成或执行任意 HTML。
func PaginateMarkdown(markdown string) []Document {
	blocks := parseMarkdownBlocks(markdown)
	if len(blocks) == 0 {
		return nil
	}
	title := "Codex 回复"
	if blocks[0].Kind == "heading" && utf8.RuneCountInString(blocks[0].Text) <= 36 {
		title = blocks[0].Text
		blocks = blocks[1:]
	}
	if len(blocks) == 0 {
		blocks = []DocumentBlock{{Kind: "paragraph", Text: title}}
	}

	pages := paginateDocumentBlocks(blocks, documentPageUnits)
	if len(pages) == 0 || len(pages) > documentMaxPages {
		return nil
	}
	documents := make([]Document, 0, len(pages))
	for index, page := range pages {
		units := 0
		for _, block := range page {
			units += documentBlockUnits(block)
		}
		titleLines := (utf8.RuneCountInString(title) + 13) / 14
		if titleLines < 1 {
			titleLines = 1
		}
		// 页高按真实移动端字号与块间距估算，避免短文档出现大片无效留白。
		height := 480 + units*32 + (titleLines-1)*68
		if height < 1100 {
			height = 1100
		}
		if height > 2050 {
			height = 2050
		}
		documents = append(documents, Document{
			Kicker:          "WECLAW / READING MODE",
			Title:           title,
			Subtitle:        fmt.Sprintf("CODEX RESPONSE / %02d OF %02d", index+1, len(pages)),
			Blocks:          page,
			PageNumber:      index + 1,
			TotalPages:      len(pages),
			ProgressPercent: (index + 1) * 100 / len(pages),
			Footer:          "回复“文字版”可获取完整可复制原文",
			Height:          height,
		})
	}
	return documents
}

func parseMarkdownBlocks(markdown string) []DocumentBlock {
	markdown = strings.TrimSpace(strings.ReplaceAll(markdown, "\r\n", "\n"))
	if markdown == "" {
		return nil
	}
	var blocks []DocumentBlock
	var paragraph []string
	var code []string
	inCode := false
	language := ""
	flushParagraph := func() {
		if len(paragraph) == 0 {
			return
		}
		blocks = append(blocks, DocumentBlock{Kind: "paragraph", Text: cleanInlineMarkdown(strings.Join(paragraph, " "))})
		paragraph = nil
	}
	flushCode := func() {
		if len(code) == 0 {
			return
		}
		blocks = append(blocks, DocumentBlock{Kind: "code", Text: strings.Join(code, "\n"), Language: language})
		code = nil
	}

	for _, rawLine := range strings.Split(markdown, "\n") {
		trimmed := strings.TrimSpace(rawLine)
		if strings.HasPrefix(trimmed, "```") {
			if inCode {
				flushCode()
				inCode = false
				language = ""
			} else {
				flushParagraph()
				inCode = true
				language = strings.TrimSpace(strings.TrimPrefix(trimmed, "```"))
			}
			continue
		}
		if inCode {
			code = append(code, rawLine)
			continue
		}
		if trimmed == "" {
			flushParagraph()
			continue
		}
		if level, heading, ok := markdownHeading(trimmed); ok {
			flushParagraph()
			blocks = append(blocks, DocumentBlock{Kind: "heading", Prefix: level, Text: cleanInlineMarkdown(heading)})
			continue
		}
		if trimmed == "---" || trimmed == "***" || trimmed == "___" {
			flushParagraph()
			blocks = append(blocks, DocumentBlock{Kind: "rule"})
			continue
		}
		if strings.HasPrefix(trimmed, ">") {
			flushParagraph()
			blocks = append(blocks, DocumentBlock{Kind: "quote", Text: cleanInlineMarkdown(strings.TrimSpace(strings.TrimPrefix(trimmed, ">")))})
			continue
		}
		if text, ok := unorderedListText(trimmed); ok {
			flushParagraph()
			blocks = append(blocks, DocumentBlock{Kind: "bullet", Text: cleanInlineMarkdown(text)})
			continue
		}
		if match := orderedListPattern.FindStringSubmatch(trimmed); len(match) == 3 {
			flushParagraph()
			blocks = append(blocks, DocumentBlock{Kind: "ordered", Prefix: match[1], Text: cleanInlineMarkdown(match[2])})
			continue
		}
		paragraph = append(paragraph, trimmed)
	}
	if inCode {
		flushCode()
	}
	flushParagraph()
	return blocks
}

func cleanInlineMarkdown(text string) string {
	text = inlineImagePattern.ReplaceAllString(text, "$1")
	text = inlineLinkPattern.ReplaceAllString(text, "$1")
	return strings.TrimSpace(strings.NewReplacer(
		"**", "", "__", "", "~~", "", "`", "",
	).Replace(text))
}

func markdownHeading(line string) (string, string, bool) {
	for level := 3; level >= 1; level-- {
		prefix := strings.Repeat("#", level) + " "
		if strings.HasPrefix(line, prefix) {
			return fmt.Sprintf("H%d", level), strings.TrimSpace(strings.TrimPrefix(line, prefix)), true
		}
	}
	return "", "", false
}

func unorderedListText(line string) (string, bool) {
	for _, prefix := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix)), true
		}
	}
	return "", false
}

func paginateDocumentBlocks(blocks []DocumentBlock, capacity int) [][]DocumentBlock {
	var pages [][]DocumentBlock
	var current []DocumentBlock
	used := 0
	flush := func() {
		if len(current) == 0 {
			return
		}
		pages = append(pages, current)
		current = nil
		used = 0
	}
	for _, block := range blocks {
		for _, piece := range splitDocumentBlock(block, capacity) {
			units := documentBlockUnits(piece)
			// 标题至少与一段正文留在同一页，避免页面末尾出现孤立标题。
			if piece.Kind == "heading" && capacity-used < units+3 {
				flush()
			}
			if used > 0 && used+units > capacity {
				flush()
			}
			current = append(current, piece)
			used += units
		}
	}
	flush()
	return pages
}

func splitDocumentBlock(block DocumentBlock, maxUnits int) []DocumentBlock {
	if documentBlockUnits(block) <= maxUnits {
		return []DocumentBlock{block}
	}
	charsPerUnit := documentCharsPerUnit(block.Kind)
	contentUnits := maxUnits - 1
	if block.Kind == "heading" {
		contentUnits = (maxUnits - 1) / 2
	}
	if block.Kind == "code" {
		return splitCodeBlock(block, maxUnits)
	}
	maxRunes := charsPerUnit * contentUnits
	if maxRunes < charsPerUnit {
		maxRunes = charsPerUnit
	}
	runes := []rune(block.Text)
	pieces := make([]DocumentBlock, 0, (len(runes)+maxRunes-1)/maxRunes)
	for len(runes) > 0 {
		end := maxRunes
		if end > len(runes) {
			end = len(runes)
		}
		piece := block
		piece.Text = strings.TrimSpace(string(runes[:end]))
		if piece.Text != "" {
			pieces = append(pieces, piece)
		}
		runes = runes[end:]
	}
	return pieces
}

func splitCodeBlock(block DocumentBlock, maxUnits int) []DocumentBlock {
	var pieces []DocumentBlock
	var current []string
	currentUnits := 2
	charsPerUnit := documentCharsPerUnit("code")
	flush := func() {
		if len(current) > 0 {
			piece := block
			piece.Text = strings.Join(current, "\n")
			pieces = append(pieces, piece)
			current = nil
			currentUnits = 2
		}
	}
	for _, line := range strings.Split(block.Text, "\n") {
		lineRunes := []rune(line)
		maxLineRunes := charsPerUnit * (maxUnits - 2)
		for len(lineRunes) > maxLineRunes {
			flush()
			piece := block
			piece.Text = string(lineRunes[:maxLineRunes])
			pieces = append(pieces, piece)
			lineRunes = lineRunes[maxLineRunes:]
		}
		lineUnits := (len(lineRunes) + charsPerUnit - 1) / charsPerUnit
		if lineUnits < 1 {
			lineUnits = 1
		}
		if currentUnits+lineUnits > maxUnits {
			flush()
		}
		current = append(current, string(lineRunes))
		currentUnits += lineUnits
	}
	flush()
	return pieces
}

func documentBlockUnits(block DocumentBlock) int {
	if block.Kind == "rule" {
		return 2
	}
	charsPerUnit := documentCharsPerUnit(block.Kind)
	lines := 0
	for _, line := range strings.Split(block.Text, "\n") {
		lineRunes := utf8.RuneCountInString(line)
		lineUnits := (lineRunes + charsPerUnit - 1) / charsPerUnit
		if lineUnits < 1 {
			lineUnits = 1
		}
		lines += lineUnits
	}
	switch block.Kind {
	case "heading":
		return lines*2 + 1
	case "code":
		return lines + 2
	case "quote", "bullet", "ordered":
		return lines + 1
	default:
		return lines + 1
	}
}

func documentCharsPerUnit(kind string) int {
	switch kind {
	case "heading":
		return 22
	case "code":
		return 48
	case "bullet", "ordered", "quote":
		return 30
	default:
		return 32
	}
}
