package memdir

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

const (
	MaxRecallFiles = 5
	MaxRecallLines = 200
	MaxRecallBytes = 25 * 1024
)

type MemoryHeader struct {
	Path        string
	Relative    string
	Filename    string
	Title       string
	Description string
	Type        MemoryType
	ModTimeUnix int64
}

type RelevantMemory struct {
	Header  MemoryHeader
	Content string
}

func MemoryDir(dataDir, workspacePath string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "projects", WorkspaceSlug(workspacePath), "memory")
}

func ScanMemoryFiles(ctx context.Context, dataDir, workspacePath string) ([]MemoryHeader, error) {
	dir := MemoryDir(dataDir, workspacePath)
	if dir == "" {
		return nil, nil
	}
	entries := []MemoryHeader{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") && path != dir {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.EqualFold(filepath.Ext(path), ".md") || strings.EqualFold(filepath.Base(path), "MEMORY.md") {
			return nil
		}
		body, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		fm, _, parseErr := DecodeFrontmatter(string(body))
		if parseErr != nil {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			rel = filepath.Base(path)
		}
		entries = append(entries, MemoryHeader{
			Path:        path,
			Relative:    filepath.ToSlash(rel),
			Filename:    filepath.Base(path),
			Title:       fm.Name,
			Description: fm.Description,
			Type:        fm.Type,
			ModTimeUnix: info.ModTime().Unix(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Relative < entries[j].Relative
	})
	return entries, nil
}

func FormatMemoryManifest(headers []MemoryHeader) string {
	if len(headers) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, h := range headers {
		fmt.Fprintf(&b, "- %s | %s | %s | %s\n", h.Relative, h.Type, h.Title, h.Description)
	}
	return strings.TrimRight(b.String(), "\n")
}

func FindRelevantMemories(ctx context.Context, dataDir, workspacePath, query string, alreadySurfaced map[string]struct{}) ([]RelevantMemory, error) {
	queryTerms := tokenizeMemoryText(query)
	if len(queryTerms) == 0 {
		return nil, nil
	}
	headers, err := ScanMemoryFiles(ctx, dataDir, workspacePath)
	if err != nil {
		return nil, err
	}
	type scored struct {
		header MemoryHeader
		score  int
	}
	scoredHeaders := make([]scored, 0, len(headers))
	for _, header := range headers {
		if _, ok := alreadySurfaced[filepath.Clean(header.Path)]; ok {
			continue
		}
		score := memoryHeaderScore(header, queryTerms)
		if score <= 0 {
			continue
		}
		scoredHeaders = append(scoredHeaders, scored{header: header, score: score})
	}
	sort.Slice(scoredHeaders, func(i, j int) bool {
		if scoredHeaders[i].score != scoredHeaders[j].score {
			return scoredHeaders[i].score > scoredHeaders[j].score
		}
		if scoredHeaders[i].header.ModTimeUnix != scoredHeaders[j].header.ModTimeUnix {
			return scoredHeaders[i].header.ModTimeUnix > scoredHeaders[j].header.ModTimeUnix
		}
		return scoredHeaders[i].header.Relative < scoredHeaders[j].header.Relative
	})
	if len(scoredHeaders) > MaxRecallFiles {
		scoredHeaders = scoredHeaders[:MaxRecallFiles]
	}
	result := make([]RelevantMemory, 0, len(scoredHeaders))
	for _, item := range scoredHeaders {
		content, readErr := ReadMemoryForRecall(item.header.Path)
		if readErr != nil {
			continue
		}
		result = append(result, RelevantMemory{Header: item.header, Content: content})
	}
	return result, nil
}

func memoryHeaderScore(header MemoryHeader, terms map[string]struct{}) int {
	headerTerms := tokenizeMemoryText(strings.Join([]string{
		header.Relative,
		header.Filename,
		header.Title,
		header.Description,
		string(header.Type),
	}, " "))
	score := 0
	for term := range terms {
		if _, ok := headerTerms[term]; ok {
			score += 3
		}
		for headerTerm := range headerTerms {
			if len(term) >= 4 && strings.Contains(headerTerm, term) {
				score++
			}
		}
	}
	return score
}

func tokenizeMemoryText(s string) map[string]struct{} {
	result := map[string]struct{}{}
	var b strings.Builder
	addToken := func(token string) {
		if len([]rune(token)) < 2 || memoryStopWords[token] {
			return
		}
		result[token] = struct{}{}
		runes := []rune(token)
		if containsCJK(runes) {
			for i := 0; i+1 < len(runes); i++ {
				gram := string(runes[i : i+2])
				if !memoryStopWords[gram] {
					result[gram] = struct{}{}
				}
			}
		}
	}
	flush := func() {
		if b.Len() == 0 {
			return
		}
		token := strings.ToLower(b.String())
		b.Reset()
		addToken(token)
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r > unicode.MaxASCII {
			b.WriteRune(unicode.ToLower(r))
			continue
		}
		flush()
	}
	flush()
	return result
}

func containsCJK(runes []rune) bool {
	for _, r := range runes {
		if r >= 0x3400 && r <= 0x9fff {
			return true
		}
	}
	return false
}

var memoryStopWords = map[string]bool{
	"the": true, "and": true, "for": true, "with": true, "this": true, "that": true,
	"you": true, "your": true, "from": true, "into": true, "what": true, "when": true,
	"怎么": true, "什么": true, "这个": true, "那个": true, "当前": true,
}

func ReadMemoryForRecall(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	fm, body, parseErr := DecodeFrontmatter(string(data))
	if parseErr == nil {
		data = []byte(body)
	}
	if len(data) > MaxRecallBytes {
		data = data[:MaxRecallBytes+1]
	}
	content := string(data)
	lines := strings.Split(content, "\n")
	truncated := len(data) > MaxRecallBytes || len(lines) > MaxRecallLines
	if len(lines) > MaxRecallLines {
		lines = lines[:MaxRecallLines]
	}
	content = strings.TrimSpace(strings.Join(lines, "\n"))
	if parseErr == nil {
		var b strings.Builder
		fmt.Fprintf(&b, "Memory: %s\n", fm.Name)
		if fm.Description != "" {
			fmt.Fprintf(&b, "Description: %s\n", fm.Description)
		}
		if content != "" {
			b.WriteString("\n")
			b.WriteString(content)
		}
		content = b.String()
	}
	if truncated {
		content += fmt.Sprintf("\n\n[Memory truncated at %d lines / %d bytes; read %s for full content.]", MaxRecallLines, MaxRecallBytes, path)
	}
	return content, nil
}
