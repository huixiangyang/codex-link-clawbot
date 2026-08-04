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
		"> 原始配置没有被修改。",
		"",
		"```go",
		"func main() {",
		"    println(\"ok\")",
		"}",
		"```",
	}, "\n")

	documents := PaginateMarkdown(markdown)
	if len(documents) != 1 {
		t.Fatalf("documents = %d, want 1", len(documents))
	}
	document := documents[0]
	if document.Title != "发布检查结果" || document.PageNumber != 1 || document.TotalPages != 1 || document.ProgressPercent != 100 {
		t.Fatalf("document metadata = %#v", document)
	}
	kinds := make(map[string]bool)
	for _, block := range document.Blocks {
		kinds[block.Kind] = true
		if strings.Contains(block.Text, "**") || strings.Contains(block.Text, "](https://") {
			t.Fatalf("inline markdown was not cleaned: %#v", block)
		}
	}
	for _, kind := range []string{"paragraph", "heading", "bullet", "ordered", "quote", "code"} {
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
		if document.Height < 1100 || document.Height > 2050 {
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

func TestPaginateMarkdownRejectsAnExcessiveImageBurst(t *testing.T) {
	paragraphs := make([]string, 80)
	for index := range paragraphs {
		paragraphs[index] = strings.Repeat("超长回复", 120)
	}
	if documents := PaginateMarkdown(strings.Join(paragraphs, "\n\n")); documents != nil {
		t.Fatalf("excessive response produced %d image pages", len(documents))
	}
}
