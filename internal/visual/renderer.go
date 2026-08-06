package visual

import (
	"bytes"
	"context"
	"embed"
	"fmt"
	"html/template"
	"image"
	_ "image/png"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	CanvasWidth       = 1080
	minCanvasHeight   = 900
	maxCanvasHeight   = 2400
	minDocumentHeight = 720
	maxDocumentHeight = 2200
	maxRenderBytes    = 12 << 20
	renderTimeout     = 12 * time.Second
)

type Variant string

type Theme string

const (
	VariantHome     Variant = "home"
	VariantSession  Variant = "session"
	VariantProgress Variant = "progress"
	VariantSystem   Variant = "system"
	VariantSuccess  Variant = "success"
	VariantWarning  Variant = "warning"
	VariantNeutral  Variant = "neutral"
)

const (
	ThemeDay   Theme = "day"
	ThemeNight Theme = "night"
)

type Fact struct {
	Label string
	Value string
}

type Option struct {
	Number       string
	Label        string
	DisplayLabel string
	Meta         string
}

// Card 是微信视觉回复的结构化输入，所有文本由 html/template 自动转义。
type Card struct {
	Variant       Variant
	Theme         Theme
	Style         Style
	Title         string
	Subtitle      string
	Facts         []Fact
	Body          []string
	Options       []Option
	Footer        string
	Height        int
	FactColumns   int
	OptionColumns int
}

// Artifact 是一次私有渲染结果，调用方发送完成后必须 Cleanup。
type Artifact struct {
	Path    string
	Width   int
	Height  int
	Cleanup func()
}

type Config struct {
	BrowserCommand string
	RootDir        string
	MaxConcurrent  int
	Now            func() time.Time
}

type Renderer struct {
	browser string
	rootDir string
	tmpl    *template.Template
	sem     chan struct{}
	now     func() time.Time
}

//go:embed assets/*.html
var assets embed.FS

func NewRenderer(cfg Config) (*Renderer, error) {
	browser, err := ResolveBrowser(cfg.BrowserCommand)
	if err != nil {
		return nil, err
	}
	if cfg.RootDir == "" {
		home, homeErr := os.UserHomeDir()
		if homeErr != nil {
			return nil, fmt.Errorf("resolve visual render root: %w", homeErr)
		}
		cfg.RootDir = filepath.Join(home, ".weclaw", "renders")
	}
	if !filepath.IsAbs(cfg.RootDir) {
		return nil, fmt.Errorf("visual render root must be absolute")
	}
	if err := os.MkdirAll(cfg.RootDir, 0o700); err != nil {
		return nil, fmt.Errorf("create visual render root: %w", err)
	}
	if err := os.Chmod(cfg.RootDir, 0o700); err != nil {
		return nil, fmt.Errorf("protect visual render root: %w", err)
	}
	if cfg.MaxConcurrent <= 0 {
		cfg.MaxConcurrent = 2
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	tmpl, err := template.New("visual").ParseFS(assets, "assets/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse visual card template: %w", err)
	}
	return &Renderer{
		browser: browser,
		rootDir: filepath.Clean(cfg.RootDir),
		tmpl:    tmpl,
		sem:     make(chan struct{}, cfg.MaxConcurrent),
		now:     cfg.Now,
	}, nil
}

func (r *Renderer) BrowserCommand() string {
	return r.browser
}

func (r *Renderer) Render(ctx context.Context, card Card) (*Artifact, error) {
	card = prepareCard(card, r.currentTime())
	htmlBytes, err := r.renderHTML(card)
	if err != nil {
		return nil, err
	}
	return r.renderArtifact(ctx, "card-*", card.Height, htmlBytes)
}

func (r *Renderer) RenderDocument(ctx context.Context, document Document) (*Artifact, error) {
	document = prepareDocument(document, r.currentTime())
	htmlBytes, err := r.renderDocumentHTML(document)
	if err != nil {
		return nil, err
	}
	return r.renderArtifact(ctx, "document-*", document.Height, htmlBytes)
}

func (r *Renderer) currentTime() time.Time {
	if r.now == nil {
		return time.Now()
	}
	return r.now()
}

func (r *Renderer) renderArtifact(ctx context.Context, pattern string, height int, htmlBytes []byte) (*Artifact, error) {
	select {
	case r.sem <- struct{}{}:
		defer func() { <-r.sem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}

	dir, err := os.MkdirTemp(r.rootDir, pattern)
	if err != nil {
		return nil, fmt.Errorf("create visual render directory: %w", err)
	}
	cleanup := func() { _ = os.RemoveAll(dir) }

	htmlPath := filepath.Join(dir, "card.html")
	pngPath := filepath.Join(dir, "card.png")
	profileDir := filepath.Join(dir, "profile")
	if err := os.Mkdir(profileDir, 0o700); err != nil {
		cleanup()
		return nil, fmt.Errorf("create chromium profile: %w", err)
	}
	if err := os.WriteFile(htmlPath, htmlBytes, 0o600); err != nil {
		cleanup()
		return nil, fmt.Errorf("write visual card HTML: %w", err)
	}

	renderCtx, cancel := context.WithTimeout(ctx, renderTimeout)
	defer cancel()
	args := []string{
		"--headless=new",
		"--no-sandbox",
		"--disable-gpu",
		"--disable-dev-shm-usage",
		"--disable-extensions",
		"--disable-background-networking",
		"--disable-sync",
		"--metrics-recording-only",
		"--no-first-run",
		"--no-default-browser-check",
		"--hide-scrollbars",
		"--host-resolver-rules=MAP * ~NOTFOUND",
		"--user-data-dir=" + profileDir,
		fmt.Sprintf("--window-size=%d,%d", CanvasWidth, height),
		"--screenshot=" + pngPath,
		(&url.URL{Scheme: "file", Path: htmlPath}).String(),
	}
	cmd := exec.CommandContext(renderCtx, r.browser, args...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		cleanup()
		detail := strings.TrimSpace(output.String())
		if len(detail) > 800 {
			detail = detail[len(detail)-800:]
		}
		return nil, fmt.Errorf("render visual card: %w: %s", err, detail)
	}
	if renderCtx.Err() != nil {
		cleanup()
		return nil, fmt.Errorf("render visual card: %w", renderCtx.Err())
	}
	if err := os.Chmod(pngPath, 0o600); err != nil {
		cleanup()
		return nil, fmt.Errorf("protect rendered card: %w", err)
	}
	info, err := os.Stat(pngPath)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("inspect rendered card: %w", err)
	}
	if info.Size() == 0 || info.Size() > maxRenderBytes {
		cleanup()
		return nil, fmt.Errorf("rendered card has invalid size %d", info.Size())
	}
	file, err := os.Open(pngPath)
	if err != nil {
		cleanup()
		return nil, fmt.Errorf("open rendered card: %w", err)
	}
	imageConfig, _, decodeErr := image.DecodeConfig(file)
	_ = file.Close()
	if decodeErr != nil {
		cleanup()
		return nil, fmt.Errorf("decode rendered card: %w", decodeErr)
	}
	if imageConfig.Width != CanvasWidth || imageConfig.Height != height {
		cleanup()
		return nil, fmt.Errorf("rendered image dimensions are %dx%d, expected %dx%d", imageConfig.Width, imageConfig.Height, CanvasWidth, height)
	}
	return &Artifact{Path: pngPath, Width: imageConfig.Width, Height: imageConfig.Height, Cleanup: cleanup}, nil
}

func (r *Renderer) renderHTML(card Card) ([]byte, error) {
	var output bytes.Buffer
	if err := r.tmpl.ExecuteTemplate(&output, cardTemplateName(card.Style), card); err != nil {
		return nil, fmt.Errorf("execute visual card template: %w", err)
	}
	return output.Bytes(), nil
}

func (r *Renderer) renderDocumentHTML(document Document) ([]byte, error) {
	var output bytes.Buffer
	if err := r.tmpl.ExecuteTemplate(&output, documentTemplateName(document.Style), document); err != nil {
		return nil, fmt.Errorf("execute visual document template: %w", err)
	}
	return output.Bytes(), nil
}

func normalizeCard(card Card) Card {
	card.Style = NormalizeStyle(card.Style)
	if card.Variant == "" {
		card.Variant = VariantNeutral
	}
	if strings.TrimSpace(card.Title) == "" {
		card.Title = "WeClaw"
	}
	if card.Theme != ThemeDay && card.Theme != ThemeNight {
		card.Theme = ThemeNight
	}
	card.FactColumns = factColumns(card)
	card.OptionColumns = optionColumns(card)
	for index := range card.Options {
		if card.Options[index].DisplayLabel == "" {
			card.Options[index].DisplayLabel, card.Options[index].Meta = splitOptionMeta(card.Options[index].Label)
		}
	}
	if card.Height <= 0 {
		card.Height = calculateCardHeight(card)
	}
	if card.Height < minCanvasHeight {
		card.Height = minCanvasHeight
	}
	if card.Height > maxCanvasHeight {
		card.Height = maxCanvasHeight
	}
	return card
}

func prepareCard(card Card, now time.Time) Card {
	if card.Theme != ThemeDay && card.Theme != ThemeNight {
		card.Theme = ThemeForTime(now)
	}
	return normalizeCard(card)
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

func factColumns(card Card) int {
	if card.FactColumns >= 1 && card.FactColumns <= 3 {
		return card.FactColumns
	}
	if len(card.Facts) <= 2 {
		return 2
	}
	for _, fact := range card.Facts {
		if len([]rune(fact.Value)) > 18 {
			return 2
		}
	}
	return 3
}

func optionColumns(card Card) int {
	if card.OptionColumns == 1 || card.OptionColumns == 2 {
		return card.OptionColumns
	}
	if len(card.Options) < 4 {
		return 1
	}
	for _, option := range card.Options {
		if len([]rune(option.Label)) > 12 {
			return 1
		}
	}
	return 2
}

func splitOptionMeta(label string) (string, string) {
	index := strings.LastIndex(label, " · ")
	if index <= 0 {
		return label, ""
	}
	meta := strings.TrimSpace(label[index+len(" · "):])
	if meta == "" || len([]rune(meta)) > 10 {
		return label, ""
	}
	return strings.TrimSpace(label[:index]), meta
}

func calculateCardHeight(card Card) int {
	titleLines := runeLines(card.Title, 15)
	height := 320 + (titleLines-1)*72
	if card.Subtitle != "" {
		height += 42 + runeLines(card.Subtitle, 28)*34
	}
	if len(card.Facts) > 0 {
		rows := (len(card.Facts) + card.FactColumns - 1) / card.FactColumns
		for row := 0; row < rows; row++ {
			lines := 1
			for column := 0; column < card.FactColumns; column++ {
				index := row*card.FactColumns + column
				if index >= len(card.Facts) {
					break
				}
				width := 17
				if card.FactColumns == 2 {
					width = 27
				}
				if valueLines := runeLines(card.Facts[index].Value, width); valueLines > lines {
					lines = valueLines
				}
			}
			height += 76 + lines*30
		}
		height += (rows-1)*12 + 24
	}
	if len(card.Body) > 0 {
		for _, line := range card.Body {
			height += runeLines(line, 31)*42 + 8
		}
		height += 18
	}
	if len(card.Options) > 0 {
		rows := (len(card.Options) + card.OptionColumns - 1) / card.OptionColumns
		for row := 0; row < rows; row++ {
			lines := 1
			for column := 0; column < card.OptionColumns; column++ {
				index := row*card.OptionColumns + column
				if index >= len(card.Options) {
					break
				}
				width := 28
				if card.OptionColumns == 2 {
					width = 12
				}
				if optionLines := runeLines(card.Options[index].DisplayLabel, width); optionLines > lines {
					lines = optionLines
				}
			}
			height += 60 + lines*34
		}
		height += (rows-1)*12 + 12
		// 单列选项的文字与状态标签更高，为页脚保留完整安全区。
		if card.OptionColumns == 1 {
			height += 86
		}
	}
	return height
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
	document.Style = NormalizeStyle(document.Style)
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

func ResolveBrowser(explicit string) (string, error) {
	if explicit != "" {
		return validateBrowser(explicit)
	}
	home, _ := os.UserHomeDir()
	var playwrightCandidates []string
	if home != "" {
		patterns := []string{
			filepath.Join(home, ".cache", "ms-playwright", "chromium-*", "chrome-linux*", "chrome"),
			filepath.Join(home, ".cache", "ms-playwright", "chromium_headless_shell-*", "chrome-headless-shell-linux*", "chrome-headless-shell"),
		}
		for _, pattern := range patterns {
			matches, _ := filepath.Glob(pattern)
			playwrightCandidates = append(playwrightCandidates, matches...)
		}
	}
	// Playwright revision is embedded in the parent directory, so reverse lexical order selects the newest installed revision.
	sort.Sort(sort.Reverse(sort.StringSlice(playwrightCandidates)))
	candidates := append(playwrightCandidates,
		"/usr/bin/google-chrome-stable",
		"/usr/bin/google-chrome",
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
	)
	for _, candidate := range candidates {
		resolved, err := validateBrowser(candidate)
		if err == nil {
			return resolved, nil
		}
	}
	return "", fmt.Errorf("no non-Snap Chromium found; install one with `npx playwright install chromium` or set visual.browser_command")
}

func validateBrowser(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("visual browser command must be absolute")
	}
	path = filepath.Clean(path)
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("visual browser command is unavailable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("visual browser command is not executable: %s", path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve visual browser command: %w", err)
	}
	if isSnapBrowserPath(path) || isSnapBrowserPath(resolved) {
		return "", fmt.Errorf("Snap Chromium is not supported because its private mount hides rendered files")
	}
	return path, nil
}

func isSnapBrowserPath(path string) bool {
	path = filepath.Clean(path)
	return path == "/snap" || strings.HasPrefix(path, "/snap/")
}
