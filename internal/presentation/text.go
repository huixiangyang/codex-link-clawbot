package presentation

import (
	"fmt"
	"regexp"
	"strings"
	"time"
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

func Elapsed(value time.Duration) string {
	if value < time.Minute {
		seconds := int(value.Round(time.Second).Seconds())
		if seconds < 1 {
			seconds = 1
		}
		return fmt.Sprintf("%d 秒", seconds)
	}
	minutes := int(value / time.Minute)
	seconds := int((value % time.Minute).Round(time.Second).Seconds())
	if seconds == 60 {
		minutes++
		seconds = 0
	}
	if seconds == 0 {
		return fmt.Sprintf("%d 分钟", minutes)
	}
	return fmt.Sprintf("%d 分 %d 秒", minutes, seconds)
}
