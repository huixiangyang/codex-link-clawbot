package visual

import (
	"html/template"
)

// lucideIcon 只返回内置 Lucide 路径，模板不会接收用户提供的 SVG 或 HTML。
func lucideIcon(name string) template.HTML {
	icons := map[string]string{
		"terminal":         `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m4 17 6-5-6-5M12 19h8"/></svg>`,
		"book-open-text":   `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M2 3h6a4 4 0 0 1 4 4v14a3 3 0 0 0-3-3H2Z"/><path d="M22 3h-6a4 4 0 0 0-4 4v14a3 3 0 0 1 3-3h7Z"/><path d="M6 8h2M6 12h2M16 8h2M16 12h2"/></svg>`,
		"copy":             `<svg viewBox="0 0 24 24" aria-hidden="true"><rect width="14" height="14" x="8" y="8" rx="2"/><path d="M4 16c-1.1 0-2-.9-2-2V4c0-1.1.9-2 2-2h10c1.1 0 2 .9 2 2"/></svg>`,
		"square-check":     `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m9 11 3 3L22 4"/><path d="M21 12v7a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11"/></svg>`,
		"square":           `<svg viewBox="0 0 24 24" aria-hidden="true"><rect width="18" height="18" x="3" y="3" rx="2"/></svg>`,
		"circle-check-big": `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M22 11.1V12a10 10 0 1 1-5.9-9.1"/><path d="m9 11 3 3L22 4"/></svg>`,
	}
	icon, exists := icons[name]
	if !exists {
		return ""
	}
	return template.HTML(icon)
}
