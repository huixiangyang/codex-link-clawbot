package logging

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

const maxEntryBytes = 16 << 10

var (
	urlPattern    = regexp.MustCompile(`https?://[^\s<>"']+`)
	bearerPattern = regexp.MustCompile(`(?i)\bBearer[ \t]+[A-Za-z0-9._~+/=-]+`)
	secretPattern = regexp.MustCompile(`(?i)\b((?:(?:bot|context|access|refresh|management)[_-]?)?token|api[_-]?key|aes[_-]?key|remote[_-]?lock[_-]?code|password|(?:client[_-]?)?secret|authorization)\b(["']?\s*[:=]\s*)(?:"[^"\r\n]*"|'[^'\r\n]*'|[^\s,;}\]]+)`)
)

// Sanitize 不替代源头控制：不要把正文、协议原文或凭据主动交给日志。
func Sanitize(message string) string {
	message = urlPattern.ReplaceAllStringFunc(message, func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil {
			return "[redacted-url]"
		}
		u.User = nil
		if u.RawQuery != "" || u.ForceQuery {
			u.RawQuery = "redacted"
			u.ForceQuery = false
		}
		if u.Fragment != "" {
			u.Fragment = "redacted"
		}
		return u.String()
	})
	message = bearerPattern.ReplaceAllString(message, "Bearer [redacted]")
	message = secretPattern.ReplaceAllString(message, "$1$2[redacted]")
	message = strings.ReplaceAll(strings.ReplaceAll(message, "\r", `\r`), "\n", `\n`)
	message = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(message, "?"))
	if len(message) > maxEntryBytes {
		message = message[:maxEntryBytes]
		for !utf8.ValidString(message) {
			message = message[:len(message)-1]
		}
		message += " [truncated]"
	}
	return message
}
