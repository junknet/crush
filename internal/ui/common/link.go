package common

import (
	"net/url"
	"path/filepath"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/home"
)

func LinkTarget(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" || strings.ContainsAny(text, "\n\r\t ") {
		return "", false
	}
	if u, err := url.Parse(text); err == nil && u.Scheme != "" && (u.Host != "" || u.Scheme == "file") {
		return text, true
	}
	if !looksLikePath(text) {
		return "", false
	}
	return FileURL(text)
}

func FileURL(path string) (string, bool) {
	path = strings.TrimSpace(path)
	if path == "" || strings.ContainsAny(path, "\n\r\t") {
		return "", false
	}
	path = home.Long(path)
	if !filepath.IsAbs(path) {
		abs, err := filepath.Abs(path)
		if err != nil {
			return "", false
		}
		path = abs
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	return u.String(), true
}

func PathLink(style lipgloss.Style, displayPath, targetPath string) string {
	if target, ok := FileURL(targetPath); ok {
		return style.Hyperlink(target).Render(displayPath)
	}
	return style.Render(displayPath)
}

func TextLink(style lipgloss.Style, display, target string) string {
	if target == "" {
		return style.Render(display)
	}
	return style.Hyperlink(target).Render(display)
}

func looksLikePath(text string) bool {
	long := home.Long(text)
	if filepath.IsAbs(long) {
		return true
	}
	if text == "~" || strings.HasPrefix(text, "~/") || strings.HasPrefix(text, "./") || strings.HasPrefix(text, "../") {
		return true
	}
	return strings.Contains(text, "/") && !strings.Contains(text, "://")
}
