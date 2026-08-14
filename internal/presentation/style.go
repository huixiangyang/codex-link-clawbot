package presentation

import "strings"

// Style 是完整的版式系统标识；具体 HTML、CSS 与 Chromium 渲染属于 visual 适配器。
type Style string

const (
	StyleEditorial Style = "editorial"
	StyleAtelier   Style = "atelier"
	StyleNoir      Style = "noir"
	StyleCute      Style = "cute"
	StyleMinimal   Style = "minimal"
	DefaultStyle         = StyleAtelier
)

type StyleDefinition struct {
	ID          Style
	Name        string
	Description string
}

var styleDefinitions = []StyleDefinition{
	{ID: StyleEditorial, Name: "刊物", Description: "纸张、衬线与克制红"},
	{ID: StyleAtelier, Name: "构筑", Description: "石材、秩序与建筑网格"},
	{ID: StyleNoir, Name: "黑标", Description: "黑白、留白与香槟金"},
	{ID: StyleCute, Name: "可爱", Description: "奶油纸、圆角与柔和色"},
	{ID: StyleMinimal, Name: "简洁", Description: "留白、细线与纯粹秩序"},
}

func Styles() []StyleDefinition {
	return append([]StyleDefinition(nil), styleDefinitions...)
}

func NormalizeStyle(style Style) Style {
	if style.Valid() {
		return style
	}
	return DefaultStyle
}

func (style Style) Valid() bool {
	switch style {
	case StyleEditorial, StyleAtelier, StyleNoir, StyleCute, StyleMinimal:
		return true
	default:
		return false
	}
}

func (style Style) Definition() StyleDefinition {
	style = NormalizeStyle(style)
	for _, definition := range styleDefinitions {
		if definition.ID == style {
			return definition
		}
	}
	return styleDefinitions[1]
}

func ResolveStyle(value string) (Style, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, definition := range styleDefinitions {
		if value == string(definition.ID) || value == strings.ToLower(definition.Name) {
			return definition.ID, true
		}
	}
	switch value {
	case "编辑", "编辑部", "杂志":
		return StyleEditorial, true
	case "建筑", "工作室":
		return StyleAtelier, true
	case "黑金", "奢华":
		return StyleNoir, true
	case "软萌", "奶油", "甜美":
		return StyleCute, true
	case "极简", "简约", "纯净":
		return StyleMinimal, true
	default:
		return "", false
	}
}
