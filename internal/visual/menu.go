package visual

import (
	"context"
	"fmt"
	"strings"
)

const menuCanvasWidth = 640

type MenuOption struct {
	Number  int
	Label   string
	Detail  string
	Current bool
}

// Menu 是可用单个数字操作的一页菜单，业务绑定留在 bridge，不进入图片。
type Menu struct {
	Title    string
	Context  string
	Notice   string
	Page     string
	Options  []MenuOption
	Previous bool
	Next     bool
	Height   int
}

func prepareMenu(menu Menu) Menu {
	menu.Options = append([]MenuOption(nil), menu.Options...)
	menu.Title = menuExcerpt(menu.Title, 12)
	menu.Context = menuExcerpt(menu.Context, 29)
	menu.Notice = menuExcerpt(menu.Notice, 66)
	// 单页固定最多六项，列表页最多四项；画布按内容收缩，不用留白填满长图。
	menu.Height = 136
	if menu.Notice != "" {
		menu.Height += 72
	}
	for i := range menu.Options {
		option := &menu.Options[i]
		option.Label = menuExcerpt(option.Label, 24)
		option.Detail = menuExcerpt(option.Detail, 28)
		menu.Height += 44
		if option.Detail != "" {
			menu.Height += 20
		}
	}
	if menu.Previous || menu.Next {
		menu.Height += 30
	}
	return menu
}

func (r *Renderer) RenderMenu(ctx context.Context, menu Menu) (*Artifact, error) {
	menu = prepareMenu(menu)
	html, err := r.renderMenuHTML(menu)
	if err != nil {
		return nil, err
	}
	return r.renderArtifactSized(ctx, "menu-*", menuCanvasWidth, menu.Height*2, html)
}

func (r *Renderer) renderMenuHTML(menu Menu) ([]byte, error) {
	if len(menu.Options) > 6 {
		return nil, fmt.Errorf("menu exceeds six options")
	}
	seen := map[int]bool{}
	for _, option := range menu.Options {
		if option.Number < 1 || option.Number > 6 || seen[option.Number] {
			return nil, fmt.Errorf("invalid menu number")
		}
		seen[option.Number] = true
	}
	menu = prepareMenu(menu)
	var output strings.Builder
	if err := r.tmpl.ExecuteTemplate(&output, "menu", menu); err != nil {
		return nil, err
	}
	return []byte(output.String()), nil
}

func menuExcerpt(text string, limit int) string {
	runes := []rune(strings.Join(strings.Fields(text), " "))
	if len(runes) > limit {
		return string(runes[:limit-1]) + "…"
	}
	return string(runes)
}
