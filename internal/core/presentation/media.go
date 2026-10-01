package presentation

import (
	"regexp"
	"strings"
)

var markdownImage = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)

// ExtractImageURLs 只收集回答中的远端图片引用，不把本机路径作为交付物。
func ExtractImageURLs(text string) []string {
	var urls []string
	for _, match := range markdownImage.FindAllStringSubmatch(text, -1) {
		url := strings.TrimSpace(match[1])
		if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
			urls = append(urls, url)
		}
	}
	return urls
}
