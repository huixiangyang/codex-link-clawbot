package visual

import (
	"strings"
	"testing"
)

func TestPaginateMarkdownPreservesReadableStructure(t *testing.T) {
	markdown := strings.Join([]string{
		"# 发布检查结果",
		"",
		"这是 **第一段**，包含 [项目链接](https://example.com/project)。",
		"",
		"## 结论",
		"- 测试已经通过",
		"- 服务保持运行",
		"1. 检查健康端点",
		"- [x] 完成自动化验证",
		"- [ ] 完成微信真机验收",
		"> 原始配置没有被修改。",
		"",
		"| 范围 | 状态 |",
		"| --- | --- |",
		"| 代码 | 通过 |",
		"| 真机 | 待验收 |",
		"",
		"```go",
		"func main() {",
		"    println(\"ok\")",
		"}",
		"```",
	}, "\n")

	documents := PaginateMarkdown(markdown)
	if len(documents) != 1 {
		t.Fatalf("documents = %d, want one dense mixed-content page", len(documents))
	}
	document := documents[0]
	if document.Title != "发布检查结果" || document.PageNumber != 1 || document.TotalPages != len(documents) || !document.FirstPage || document.MultiPage != (len(documents) > 1) {
		t.Fatalf("document metadata = %#v", document)
	}
	kinds := make(map[string]bool)
	for _, page := range documents {
		for _, block := range page.Blocks {
			kinds[block.Kind] = true
			if strings.Contains(block.Text, "**") || strings.Contains(block.Text, "](https://") {
				t.Fatalf("inline markdown was not cleaned: %#v", block)
			}
		}
	}
	for _, kind := range []string{"paragraph", "heading", "bullet", "ordered", "task", "quote", "table", "code"} {
		if !kinds[kind] {
			t.Fatalf("missing %s block: %#v", kind, document.Blocks)
		}
	}
}

func TestPaginateMarkdownCreatesBoundedPages(t *testing.T) {
	paragraphs := make([]string, 18)
	for index := range paragraphs {
		paragraphs[index] = strings.Repeat("适合移动端阅读的内容", 18)
	}
	documents := PaginateMarkdown(strings.Join(paragraphs, "\n\n"))
	if len(documents) < 2 || len(documents) > documentMaxPages {
		t.Fatalf("documents = %d, want 2..%d", len(documents), documentMaxPages)
	}
	for index, document := range documents {
		if document.PageNumber != index+1 || document.TotalPages != len(documents) {
			t.Fatalf("page metadata = %#v", document)
		}
		if document.Height < minDocumentHeight || document.Height > maxDocumentHeight {
			t.Fatalf("page height = %d", document.Height)
		}
		units := 0
		for _, block := range document.Blocks {
			units += documentBlockUnits(block)
		}
		if units > documentPageUnits {
			t.Fatalf("page %d uses %d units", index+1, units)
		}
	}
}

func TestPaginateMarkdownSplitsOversizedTableRows(t *testing.T) {
	markdown := "| 名称 | 内容 |\n| --- | --- |\n| 超长项 | " + strings.Repeat("移动端表格内容", 500) + " |"
	documents := PaginateMarkdown(markdown)
	if len(documents) < 2 || len(documents) > documentMaxPages {
		t.Fatalf("documents = %d, want 2..%d", len(documents), documentMaxPages)
	}
	for pageIndex, document := range documents {
		units := 0
		for _, block := range document.Blocks {
			units += documentBlockUnits(block)
		}
		if units > documentPageUnits {
			t.Fatalf("page %d uses %d units", pageIndex+1, units)
		}
	}
}

func TestPaginateMarkdownUsesContentFirstMetadata(t *testing.T) {
	documents := PaginateMarkdown(strings.Repeat("没有一级标题的正文应该直接开始，不需要虚构一个巨大标题。", 20))
	if len(documents) == 0 || documents[0].Title != "" {
		t.Fatalf("content-first title = %#v", documents)
	}
	for index, document := range documents {
		if document.FirstPage != (index == 0) || document.LastPage != (index == len(documents)-1) || document.MultiPage != (len(documents) > 1) {
			t.Fatalf("page flags = %#v", document)
		}
		if index < len(documents)-1 && document.Footer != "" {
			t.Fatalf("non-final page footer = %q", document.Footer)
		}
	}
	if documents[len(documents)-1].Footer == "" {
		t.Fatal("final page must retain the copyable-text hint")
	}
}

func TestPaginateMarkdownRejectsAnExcessiveImageBurst(t *testing.T) {
	paragraphs := make([]string, 80)
	for index := range paragraphs {
		paragraphs[index] = strings.Repeat("超长回复", 120)
	}
	if documents := PaginateMarkdown(strings.Join(paragraphs, "\n\n")); documents != nil {
		t.Fatalf("excessive response produced %d image pages", len(documents))
	}
}
