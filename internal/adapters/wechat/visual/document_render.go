package visual

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"html/template"
	"strings"
	"sync"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/core/presentation"
)

type Theme string

const (
	CanvasWidth       = 1080
	minDocumentHeight = 720
	maxDocumentHeight = 2200
)

const (
	ThemeDay   Theme = "day"
	ThemeNight Theme = "night"
)

var (
	backgroundOnce sync.Once
	backgroundURLs map[presentation.Style]template.URL
)

// backgroundDataURL 只允许读取编译进二进制的风格纹理，并转为离线 data URL。
// 返回值由固定风格枚举决定，不接受用户路径，避免模板获得任意文件读取能力。
func backgroundDataURL(style presentation.Style) template.URL {
	style = presentation.NormalizeStyle(style)
	backgroundOnce.Do(func() {
		backgroundURLs = make(map[presentation.Style]template.URL, len(presentation.Styles()))
		for _, definition := range presentation.Styles() {
			data, err := assets.ReadFile("assets/backgrounds/" + string(definition.ID) + ".webp")
			if err != nil {
				continue
			}
			backgroundURLs[definition.ID] = template.URL("data:image/webp;base64," + base64.StdEncoding.EncodeToString(data))
		}
	})
	return backgroundURLs[style]
}

func (r *Renderer) RenderDocument(ctx context.Context, document Document) (*Artifact, error) {
	document = prepareDocument(document, r.currentTime())
	htmlBytes, err := r.renderDocumentHTML(document)
	if err != nil {
		return nil, err
	}
	return r.renderArtifactSized(ctx, "document-*", CanvasWidth, document.Height, htmlBytes)
}

func (r *Renderer) currentTime() time.Time {
	if r.now == nil {
		return time.Now()
	}
	return r.now()
}

func (r *Renderer) renderDocumentHTML(document Document) ([]byte, error) {
	var output bytes.Buffer
	if err := r.tmpl.ExecuteTemplate(&output, documentTemplateName(document.Style), document); err != nil {
		return nil, fmt.Errorf("execute visual document template: %w", err)
	}
	return output.Bytes(), nil
}

func documentTemplateName(style presentation.Style) string {
	return "document." + string(presentation.NormalizeStyle(style))
}

func prepareDocument(document Document, now time.Time) Document {
	if document.Theme != ThemeDay && document.Theme != ThemeNight {
		document.Theme = ThemeForTime(now)
	}
	return normalizeDocument(document)
}

// ThemeForTime 以服务所在时区为准，白天使用明亮主题，夜间降低环境光刺激。
func ThemeForTime(now time.Time) Theme {
	if hour := now.Hour(); hour >= 7 && hour < 19 {
		return ThemeDay
	}
	return ThemeNight
}

func runeLines(value string, width int) int {
	if width <= 0 {
		return 1
	}
	count := len([]rune(strings.TrimSpace(value)))
	if count <= 0 {
		return 1
	}
	return (count + width - 1) / width
}

func normalizeDocument(document Document) Document {
	document.Style = presentation.NormalizeStyle(document.Style)
	if document.Theme != ThemeDay && document.Theme != ThemeNight {
		document.Theme = ThemeNight
	}
	document.Title = strings.TrimSpace(document.Title)
	if document.PageNumber <= 0 {
		document.PageNumber = 1
	}
	if document.TotalPages < document.PageNumber {
		document.TotalPages = document.PageNumber
	}
	document.MultiPage = document.TotalPages > 1
	document.FirstPage = document.PageNumber == 1
	document.LastPage = document.PageNumber == document.TotalPages
	if document.Height < minDocumentHeight {
		document.Height = minDocumentHeight
	}
	if document.Height > maxDocumentHeight {
		document.Height = maxDocumentHeight
	}
	return document
}
