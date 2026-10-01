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
	"strings"
	"time"

	"github.com/huixiangyang/codex-link-clawbot/internal/platform/statefile"
)

const (
	maxRenderBytes = 12 << 20
	renderTimeout  = 12 * time.Second
)

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

//go:embed assets/*.html assets/backgrounds/*.webp
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
		cfg.RootDir = filepath.Join(home, ".codex-link-clawbot", "tmp", "render")
	}
	if !filepath.IsAbs(cfg.RootDir) {
		return nil, fmt.Errorf("visual render root must be absolute")
	}
	if err := statefile.EnsurePrivateDirectory(cfg.RootDir); err != nil {
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
	tmpl, err := template.New("visual").Funcs(template.FuncMap{
		"lucide":     lucideIcon,
		"background": backgroundDataURL,
	}).ParseFS(assets, "assets/*.html")
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

// renderArtifactSized 为菜单图和阅读图提供独立尺寸的离线渲染。
func (r *Renderer) renderArtifactSized(ctx context.Context, pattern string, width, height int, htmlBytes []byte) (*Artifact, error) {
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
		fmt.Sprintf("--window-size=%d,%d", width, height),
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
	if imageConfig.Width != width || imageConfig.Height != height {
		cleanup()
		return nil, fmt.Errorf("rendered image dimensions are %dx%d, expected %dx%d", imageConfig.Width, imageConfig.Height, width, height)
	}
	return &Artifact{Path: pngPath, Width: imageConfig.Width, Height: imageConfig.Height, Cleanup: cleanup}, nil
}
