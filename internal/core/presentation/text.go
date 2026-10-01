package presentation

import (
	"regexp"
	"strings"
)

var (
	activityURLPattern         = regexp.MustCompile(`(?i)https?://[^\s，。！？；;]+`)
	activityUnixPathPattern    = regexp.MustCompile(`/[^\s，。！？；;]+`)
	activityWindowsPathPattern = regexp.MustCompile(`(?i)[a-z]:[\\/][^\s，。！？；;]+`)
)

// SanitizeActivity 只保留适合移动端阶段展示的摘要，隐藏链接和本机路径。
func SanitizeActivity(value string) string {
	value = activityURLPattern.ReplaceAllString(value, "[链接]")
	value = activityWindowsPathPattern.ReplaceAllString(value, "[本机路径]")
	value = activityUnixPathPattern.ReplaceAllString(value, "[本机路径]")
	return strings.Join(strings.Fields(value), " ")
}

func Truncate(value string, limit int) string {
	runes := []rune(value)
	if limit <= 0 || len(runes) <= limit {
		return value
	}
	if limit == 1 {
		return "…"
	}
	return string(runes[:limit-1]) + "…"
}

// NormalizeLine 将菜单摘要和提供商错误统一为有长度限制的单行文本。
func NormalizeLine(value string, limit int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	if limit == 1 {
		return "…"
	}
	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}
