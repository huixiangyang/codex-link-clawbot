package visual

import (
	"fmt"
	"github.com/huixiangyang/codex-link-clawbot/internal/presentation"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	documentPageUnits = 40
	documentMaxPages  = 10
)

type DocumentBlock struct {
	Kind     string
	Text     string
	Prefix   string
	Language string
	Columns  []string
	Rows     [][]string
}

type Document struct {
	Theme      Theme
	Style      presentation.Style
	Title      string
	Blocks     []DocumentBlock
	PageNumber int
	TotalPages int
	FirstPage  bool
	LastPage   bool
	MultiPage  bool
	Footer     string
	Height     int
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
	title := ""
	if blocks[0].Kind == "heading" && utf8.RuneCountInString(blocks[0].Text) <= 36 {
		title = blocks[0].Text
		blocks = blocks[1:]
	}
	if len(blocks) == 0 {
		blocks = []DocumentBlock{{Kind: "paragraph", Text: title}}
		title = ""
	}

	pages := paginateDocumentBlocks(blocks, documentPageUnits)
	if len(pages) == 0 || len(pages) > documentMaxPages {
		return nil
	}
	documents := make([]Document, 0, len(pages))
	for index, page := range pages {
		firstPage := index == 0
		lastPage := index == len(pages)-1
		// 高度按模板实际排版结构估算，兼顾内容密度与 overflow:hidden 的安全余量。
		height := estimateDocumentHeight(title, page, firstPage, lastPage)
		if height < minDocumentHeight {
			height = minDocumentHeight
		}
		if height > maxDocumentHeight {
			height = maxDocumentHeight
		}
		footer := ""
		if lastPage {
			footer = "回复“文字版”获取可复制原文"
		}
		documents = append(documents, Document{
			Title: title, Blocks: page, PageNumber: index + 1, TotalPages: len(pages),
			FirstPage: firstPage, LastPage: lastPage, MultiPage: len(pages) > 1,
			Footer: footer, Height: height,
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

	lines := strings.Split(markdown, "\n")
	for lineIndex := 0; lineIndex < len(lines); lineIndex++ {
		rawLine := lines[lineIndex]
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
		if lineIndex+1 < len(lines) {
			if table, consumed, ok := parseMarkdownTable(lines[lineIndex:]); ok {
				flushParagraph()
				blocks = append(blocks, table)
				lineIndex += consumed - 1
				continue
			}
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
		if state, text, ok := markdownTaskText(trimmed); ok {
			flushParagraph()
			blocks = append(blocks, DocumentBlock{Kind: "task", Prefix: state, Text: cleanInlineMarkdown(text)})
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

func markdownTaskText(line string) (string, string, bool) {
	if len(line) < 6 || (line[0] != '-' && line[0] != '*' && line[0] != '+') || line[1] != ' ' || line[2] != '[' || line[4] != ']' || line[5] != ' ' {
		return "", "", false
	}
	switch line[3] {
	case 'x', 'X':
		return "done", strings.TrimSpace(line[6:]), true
	case ' ':
		return "open", strings.TrimSpace(line[6:]), true
	default:
		return "", "", false
	}
}

func parseMarkdownTable(lines []string) (DocumentBlock, int, bool) {
	if len(lines) < 2 {
		return DocumentBlock{}, 0, false
	}
	headings := splitMarkdownTableRow(strings.TrimSpace(lines[0]))
	if len(headings) < 2 || len(headings) > 4 || !isMarkdownTableSeparator(strings.TrimSpace(lines[1]), len(headings)) {
		return DocumentBlock{}, 0, false
	}
	for index := range headings {
		headings[index] = cleanInlineMarkdown(headings[index])
		if headings[index] == "" {
			return DocumentBlock{}, 0, false
		}
	}
	rows := make([][]string, 0)
	consumed := 2
	for consumed < len(lines) {
		trimmed := strings.TrimSpace(lines[consumed])
		if trimmed == "" {
			break
		}
		cells := splitMarkdownTableRow(trimmed)
		if len(cells) != len(headings) {
			break
		}
		for index := range cells {
			cells[index] = cleanInlineMarkdown(cells[index])
		}
		rows = append(rows, cells)
		consumed++
	}
	if len(rows) == 0 {
		return DocumentBlock{}, 0, false
	}
	return DocumentBlock{Kind: "table", Columns: headings, Rows: rows}, consumed, true
}

func splitMarkdownTableRow(line string) []string {
	line = strings.TrimSpace(line)
	if !strings.Contains(line, "|") {
		return nil
	}
	line = strings.TrimPrefix(line, "|")
	line = strings.TrimSuffix(line, "|")
	parts := strings.Split(line, "|")
	for index := range parts {
		parts[index] = strings.TrimSpace(parts[index])
	}
	return parts
}

func isMarkdownTableSeparator(line string, columns int) bool {
	cells := splitMarkdownTableRow(line)
	if len(cells) != columns {
		return false
	}
	for _, cell := range cells {
		cell = strings.TrimSpace(strings.Trim(cell, ":"))
		if len(cell) < 3 || strings.Trim(cell, "-") != "" {
			return false
		}
	}
	return true
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
	if block.Kind == "table" {
		return splitTableBlock(block, maxUnits)
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

func splitTableBlock(block DocumentBlock, maxUnits int) []DocumentBlock {
	var pieces []DocumentBlock
	current := DocumentBlock{Kind: "table", Columns: append([]string(nil), block.Columns...)}
	for _, row := range splitOversizedTableRows(block.Rows, maxUnits) {
		candidate := current
		candidate.Rows = append(append([][]string(nil), current.Rows...), append([]string(nil), row...))
		if len(current.Rows) > 0 && documentBlockUnits(candidate) > maxUnits {
			pieces = append(pieces, current)
			current = DocumentBlock{Kind: "table", Columns: append([]string(nil), block.Columns...)}
		}
		current.Rows = append(current.Rows, append([]string(nil), row...))
	}
	if len(current.Rows) > 0 {
		pieces = append(pieces, current)
	}
	return pieces
}

func splitOversizedTableRows(rows [][]string, maxUnits int) [][]string {
	columnCount := 1
	if len(rows) > 0 && len(rows[0]) > 0 {
		columnCount = len(rows[0])
	}
	visualTracks := (columnCount + 1) / 2
	maxCellRunes := ((maxUnits - 2) / visualTracks) * 16
	if maxCellRunes < 16 {
		maxCellRunes = 16
	}
	var result [][]string
	for _, row := range rows {
		remaining := make([][]rune, len(row))
		hasRowContent := false
		for index, cell := range row {
			remaining[index] = []rune(cell)
			hasRowContent = hasRowContent || len(remaining[index]) > 0
		}
		if !hasRowContent {
			result = append(result, append([]string(nil), row...))
			continue
		}
		for {
			piece := make([]string, len(row))
			hasContent := false
			for index := range remaining {
				end := len(remaining[index])
				if end > maxCellRunes {
					end = maxCellRunes
				}
				if end > 0 {
					hasContent = true
					piece[index] = string(remaining[index][:end])
					remaining[index] = remaining[index][end:]
				}
			}
			if !hasContent {
				break
			}
			result = append(result, piece)
		}
	}
	return result
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
	if block.Kind == "table" {
		units := 1
		for _, row := range block.Rows {
			rowUnits := 0
			for start := 0; start < len(row); start += 2 {
				trackUnits := 2
				end := start + 2
				if end > len(row) {
					end = len(row)
				}
				for _, cell := range row[start:end] {
					cellUnits := (utf8.RuneCountInString(cell) + 15) / 16
					if cellUnits > trackUnits {
						trackUnits = cellUnits
					}
				}
				rowUnits += trackUnits
			}
			units += rowUnits + 1
		}
		return units
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
	case "quote", "bullet", "ordered", "task":
		return lines + 1
	default:
		return lines + 1
	}
}

func estimateDocumentHeight(title string, blocks []DocumentBlock, firstPage, lastPage bool) int {
	// 页面内边距、品牌栏以及模板间的差异预算。
	height := 160
	if firstPage && title != "" {
		height += runeLines(title, 14)*68 + 26
	}
	for index, block := range blocks {
		if index > 0 {
			height += 17
		}
		height += estimateDocumentBlockHeight(block)
	}
	if lastPage {
		height += 60
	}
	return height + 36
}

func estimateDocumentBlockHeight(block DocumentBlock) int {
	switch block.Kind {
	case "rule":
		return 11
	case "heading":
		return runeLines(block.Text, 22)*47 + 6
	case "bullet", "ordered", "task":
		return runeLines(block.Text, 30) * 42
	case "quote":
		return runeLines(block.Text, 32)*40 + 38
	case "code":
		lines := 0
		for _, line := range strings.Split(block.Text, "\n") {
			lines += runeLines(line, 48)
		}
		return lines*35 + 66
	case "table":
		height := 0
		for rowIndex, row := range block.Rows {
			if rowIndex > 0 {
				height += 11
			}
			for start := 0; start < len(row); start += 2 {
				trackLines := 1
				end := start + 2
				if end > len(row) {
					end = len(row)
				}
				for _, cell := range row[start:end] {
					if lines := runeLines(cell, 16); lines > trackLines {
						trackLines = lines
					}
				}
				height += 58 + trackLines*33
			}
		}
		return height
	default:
		return runeLines(block.Text, 32) * 45
	}
}

func documentCharsPerUnit(kind string) int {
	switch kind {
	case "heading":
		return 22
	case "code":
		return 48
	case "bullet", "ordered", "quote", "task":
		return 30
	default:
		return 32
	}
}
