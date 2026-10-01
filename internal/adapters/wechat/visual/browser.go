package visual

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

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
			filepath.Join(home, "Library", "Caches", "ms-playwright", "chromium-*", "chrome-mac*", "Chromium.app", "Contents", "MacOS", "Chromium"),
			filepath.Join(home, "Library", "Caches", "ms-playwright", "chromium_headless_shell-*", "chrome-headless-shell-mac*", "chrome-headless-shell"),
			filepath.Join(home, "Library", "Caches", "ms-playwright", "chromium_headless_shell-*", "chrome-mac*", "headless_shell"),
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
