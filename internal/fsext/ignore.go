package fsext

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

var commonIgnoreNames = map[string]struct{}{
	".git":         {},
	".vscode":      {},
	"__pycache__":  {},
	"node_modules": {},
	"target":       {},
}

// DirectoryLister evaluates directory entries against Crush ignore rules.
type DirectoryLister struct {
	root string
}

// NewDirectoryLister builds a lister rooted at the workspace directory.
func NewDirectoryLister(root string) *DirectoryLister {
	return &DirectoryLister{root: filepath.Clean(root)}
}

func (d *DirectoryLister) shouldIgnore(path string, info os.FileInfo, isDir bool) bool {
	targetPath := path
	if !filepath.IsAbs(targetPath) {
		targetPath = filepath.Join(d.root, targetPath)
	}
	if info != nil {
		isDir = info.IsDir()
	}
	return shouldExcludePath(d.root, targetPath, isDir)
}

// ShouldExcludeFile reports whether path is excluded by common, .gitignore,
// or .crushignore patterns rooted at root.
func ShouldExcludeFile(root string, path string) bool {
	isDir := false
	if info, err := os.Stat(path); err == nil {
		isDir = info.IsDir()
	}
	return shouldExcludePath(root, path, isDir)
}

func shouldExcludePath(root string, path string, isDir bool) bool {
	cleanRoot := filepath.Clean(root)
	cleanPath := filepath.Clean(path)
	if cleanRoot == cleanPath {
		return false
	}

	relativePath, err := filepath.Rel(cleanRoot, cleanPath)
	if err != nil || relativePath == "." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator)) || relativePath == ".." {
		return false
	}

	if matchesCommonIgnore(relativePath) {
		return true
	}

	for _, ignoreDirectory := range ignoreDirectories(cleanRoot, cleanPath, isDir) {
		patternRelativePath, err := filepath.Rel(ignoreDirectory, cleanPath)
		if err != nil || patternRelativePath == "." || strings.HasPrefix(patternRelativePath, ".."+string(filepath.Separator)) || patternRelativePath == ".." {
			continue
		}
		if directoryIgnoreFilesMatch(ignoreDirectory, patternRelativePath, isDir) {
			return true
		}
	}
	return false
}

func matchesCommonIgnore(relativePath string) bool {
	parts := strings.FieldsFunc(filepath.ToSlash(relativePath), func(r rune) bool {
		return r == '/'
	})
	for _, part := range parts {
		if _, ok := commonIgnoreNames[part]; ok {
			return true
		}
	}
	return strings.HasSuffix(filepath.Base(relativePath), ".tmp")
}

func ignoreDirectories(root string, path string, isDir bool) []string {
	lastDirectory := path
	if !isDir {
		lastDirectory = filepath.Dir(path)
	}

	var directories []string
	for current := filepath.Clean(root); ; current = filepath.Join(current, nextPathElement(current, lastDirectory)) {
		directories = append(directories, current)
		if current == lastDirectory {
			break
		}
		if !strings.HasPrefix(lastDirectory, current+string(filepath.Separator)) {
			break
		}
	}
	return directories
}

func nextPathElement(current string, target string) string {
	remaining := strings.TrimPrefix(target, current)
	remaining = strings.TrimPrefix(remaining, string(filepath.Separator))
	if separatorIndex := strings.IndexRune(remaining, filepath.Separator); separatorIndex >= 0 {
		return remaining[:separatorIndex]
	}
	return remaining
}

func directoryIgnoreFilesMatch(ignoreDirectory string, relativePath string, isDir bool) bool {
	for _, filename := range []string{".gitignore", ".crushignore"} {
		if ignoreFileMatches(filepath.Join(ignoreDirectory, filename), relativePath, isDir) {
			return true
		}
	}
	return false
}

func ignoreFileMatches(ignoreFilePath string, relativePath string, isDir bool) bool {
	file, err := os.Open(ignoreFilePath)
	if err != nil {
		return false
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		pattern := strings.TrimSpace(scanner.Text())
		if pattern == "" || strings.HasPrefix(pattern, "#") || strings.HasPrefix(pattern, "!") {
			continue
		}
		if ignorePatternMatches(pattern, relativePath, isDir) {
			return true
		}
	}
	return false
}

func ignorePatternMatches(pattern string, relativePath string, isDir bool) bool {
	directoryOnly := strings.HasSuffix(pattern, "/")
	pattern = strings.Trim(pattern, "/")
	if pattern == "" {
		return false
	}
	if directoryOnly && !isDir {
		return false
	}

	slashRelativePath := filepath.ToSlash(relativePath)
	slashPattern := filepath.ToSlash(pattern)
	if strings.Contains(slashPattern, "/") {
		matched, err := filepath.Match(slashPattern, slashRelativePath)
		return err == nil && matched
	}

	for _, part := range strings.Split(slashRelativePath, "/") {
		matched, err := filepath.Match(slashPattern, part)
		if err == nil && matched {
			return true
		}
	}
	return false
}
