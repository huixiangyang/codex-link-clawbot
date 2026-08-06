package visual

import (
	"context"
	"fmt"
	"html/template"
	"strings"
	"time"
)

const directoryCanvasHeight = 2280

type DirectoryItem struct {
	Code  string
	Label string
	Meta  string
}

type DirectorySection struct {
	Code  string
	Title string
	Icon  string
	Items []DirectoryItem
}

// Directory 是微信大菜单的专用结构，不复用普通状态卡的扁平选项布局。
type Directory struct {
	Theme    Theme
	Style    Style
	Title    string
	Subtitle string
	Facts    []Fact
	Sections []DirectorySection
	Footer   string
	Height   int
}

func (r *Renderer) RenderDirectory(ctx context.Context, directory Directory) (*Artifact, error) {
	directory, err := prepareDirectory(directory, r.currentTime())
	if err != nil {
		return nil, err
	}
	var output strings.Builder
	if err := r.tmpl.ExecuteTemplate(&output, directoryTemplateName(directory.Style), directory); err != nil {
		return nil, fmt.Errorf("execute directory template: %w", err)
	}
	return r.renderArtifact(ctx, "directory-*", directory.Height, []byte(output.String()))
}

func prepareDirectory(directory Directory, now time.Time) (Directory, error) {
	directory.Style = NormalizeStyle(directory.Style)
	if directory.Theme != ThemeDay && directory.Theme != ThemeNight {
		if hour := now.Hour(); hour >= 7 && hour < 19 {
			directory.Theme = ThemeDay
		} else {
			directory.Theme = ThemeNight
		}
	}
	directory.Title = strings.TrimSpace(directory.Title)
	directory.Subtitle = strings.TrimSpace(directory.Subtitle)
	directory.Footer = strings.TrimSpace(directory.Footer)
	if directory.Title == "" || len(directory.Sections) != 6 {
		return Directory{}, fmt.Errorf("directory requires a title and six sections")
	}
	seen := make(map[string]bool, 48)
	for sectionIndex := range directory.Sections {
		section := &directory.Sections[sectionIndex]
		section.Code = strings.TrimSpace(section.Code)
		section.Title = strings.TrimSpace(section.Title)
		section.Icon = strings.TrimSpace(section.Icon)
		if !validDirectoryCode(section.Code) || section.Title == "" || len(section.Items) == 0 || len(section.Items) > 8 || seen[section.Code] {
			return Directory{}, fmt.Errorf("invalid directory section")
		}
		seen[section.Code] = true
		for itemIndex := range section.Items {
			item := &section.Items[itemIndex]
			item.Code = strings.TrimSpace(item.Code)
			item.Label = strings.TrimSpace(item.Label)
			item.Meta = strings.TrimSpace(item.Meta)
			if !validDirectoryCode(item.Code) || item.Label == "" || seen[item.Code] {
				return Directory{}, fmt.Errorf("invalid directory item")
			}
			seen[item.Code] = true
		}
	}
	directory.Height = directoryCanvasHeight
	return directory, nil
}

func validDirectoryCode(code string) bool {
	if len(code) < 1 || len(code) > 2 || code[0] < '1' || code[0] > '9' {
		return false
	}
	for _, character := range code[1:] {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func directoryTemplateName(style Style) string {
	return "directory." + string(NormalizeStyle(style))
}

// lucideIcon 只返回内置 Lucide 路径，模板不会接收用户提供的 SVG 或 HTML。
func lucideIcon(name string) template.HTML {
	icons := map[string]string{
		"messages-square":  `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M7 8h10M7 12h6"/><path d="M21 15a4 4 0 0 1-4 4H8l-5 3V7a4 4 0 0 1 4-4h10a4 4 0 0 1 4 4z"/></svg>`,
		"folder-kanban":    `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M4 20h16a2 2 0 0 0 2-2V8a2 2 0 0 0-2-2h-7l-2-2H4a2 2 0 0 0-2 2v12a2 2 0 0 0 2 2Z"/><path d="M8 10v6M12 10v3M16 10v5"/></svg>`,
		"list-todo":        `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m3 6 2 2 4-4M3 12l2 2 4-4M3 18l2 2 4-4M13 6h8M13 12h8M13 18h8"/></svg>`,
		"palette":          `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 22a10 10 0 1 1 10-10c0 2.8-1.5 4-3.5 4H17a2 2 0 0 0-2 2v1.5c0 1.4-1.2 2.5-3 2.5Z"/><path d="M7.5 10h.01M10.5 6.5h.01M15 7.5h.01M17 11.5h.01"/></svg>`,
		"package-open":     `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m12 3 8 4.5v9L12 21l-8-4.5v-9L12 3Z"/><path d="m4.5 7.8 7.5 4.3 7.5-4.3M12 12.1V21M8 5.2l8 4.6"/></svg>`,
		"shield-check":     `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 13c0 5-3.5 7.5-8 9-4.5-1.5-8-4-8-9V5l8-3 8 3v8Z"/><path d="m9 12 2 2 4-4"/></svg>`,
		"command":          `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M18 9a3 3 0 1 0-3-3v12a3 3 0 1 0 3-3H6a3 3 0 1 0 3 3V6a3 3 0 1 0-3 3h12Z"/></svg>`,
		"book-open-text":   `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M2 3h6a4 4 0 0 1 4 4v14a3 3 0 0 0-3-3H2Z"/><path d="M22 3h-6a4 4 0 0 0-4 4v14a3 3 0 0 1 3-3h7Z"/><path d="M6 8h2M6 12h2M16 8h2M16 12h2"/></svg>`,
		"copy":             `<svg viewBox="0 0 24 24" aria-hidden="true"><rect width="14" height="14" x="8" y="8" rx="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>`,
		"corner-down-left": `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M9 10 4 15l5 5"/><path d="M20 4v7a4 4 0 0 1-4 4H4"/></svg>`,
		"square-check":     `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m9 11 3 3L22 4"/><path d="M21 12v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11"/></svg>`,
		"square":           `<svg viewBox="0 0 24 24" aria-hidden="true"><rect width="18" height="18" x="3" y="3" rx="2"/></svg>`,
	}
	icon, exists := icons[name]
	if !exists {
		return ""
	}
	return template.HTML(icon)
}
