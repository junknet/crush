package tools

import (
	"path/filepath"
	"strings"
	"unicode"
)

// Curly quote runes that LLMs commonly emit when transcribing prose and that
// trip up exact-byte edit matching. Files written by editors (or imported
// from word processors) often contain the curly forms while the model emits
// straight quotes — or vice versa. We normalise both sides to straight
// before matching and, when a normalised match wins, copy the file's exact
// bytes back so the edit preserves the file's typography.
const (
	leftSingleCurlyQuote  = '‘' // ‘
	rightSingleCurlyQuote = '’' // ’
	leftDoubleCurlyQuote  = '“' // “
	rightDoubleCurlyQuote = '”' // ”
)

// normalizeQuotes folds curly quotes to straight quotes. The byte length is
// preserved for the ASCII case (straight quotes are one byte each) but may
// shrink for inputs that contained multi-byte curly characters. Callers must
// not assume position equivalence with the input.
func normalizeQuotes(s string) string {
	if !strings.ContainsAny(s, "‘’“”") {
		return s
	}
	r := strings.NewReplacer(
		"‘", "'",
		"’", "'",
		"“", `"`,
		"”", `"`,
	)
	return r.Replace(s)
}

// findActualString tries to locate search inside content, first by exact
// match and then by curly-quote-insensitive match. On a fuzzy hit it returns
// the *file's* exact bytes at that position so callers can use them as the
// real old_string for slicing (preserving the file's typography).
//
// Returns "" when neither path matches.
func findActualString(content, search string) string {
	if search == "" {
		return ""
	}
	if strings.Contains(content, search) {
		return search
	}
	normSearch := normalizeQuotes(search)
	normContent := normalizeQuotes(content)
	idx := strings.Index(normContent, normSearch)
	if idx < 0 {
		return ""
	}
	// Normalisation may have changed byte offsets if multi-byte curly
	// quotes were present in the prefix. Walk the original content with
	// the normalised offset to recover the actual byte index.
	actualIdx := mapNormalizedIndex(content, idx)
	if actualIdx < 0 || actualIdx+len(search) > len(content) {
		return ""
	}
	// Compute the length in the original content that corresponds to
	// normSearch's length.
	end := mapNormalizedIndex(content[actualIdx:], len(normSearch))
	if end < 0 {
		return ""
	}
	return content[actualIdx : actualIdx+end]
}

// providerSanitizationMap lists tokens that LLM provider API gateways
// rewrite on their way through the wire — most notably Anthropic's
// `<function_results>` family. The model only ever sees the rewritten
// short forms, so when it asks Edit to match `<fnr>` against a file
// whose source contains `<function_results>`, the exact match fails.
//
// We carry the reverse map and apply it as a fallback fuzzy pass after
// quote normalisation but before declaring "not found". OpenAI and Gemini
// don't do this rewriting today, but the substitution is one-directional
// (sanitized → expanded) and harmless when the file already contains the
// expanded form, so leaving it on for every provider is safe and
// future-proofs against new gateways introducing the same pattern.
var providerSanitizationMap = map[string]string{
	"<fnr>":          "<function_results>",
	"</fnr>":         "</function_results>",
	"<n>":            "<name>",
	"</n>":           "</name>",
	"<o>":            "<output>",
	"</o>":           "</output>",
	"<e>":            "<error>",
	"</e>":           "</error>",
	"<s>":            "<system>",
	"</s>":           "</system>",
	"<r>":            "<result>",
	"</r>":           "</result>",
	"< META_START >": "<META_START>",
	"< META_END >":   "<META_END>",
	"< EOT >":        "<EOT>",
	"< META >":       "<META>",
	"< SOS >":        "<SOS>",
	"\n\nH:":         "\n\nHuman:",
	"\n\nA:":         "\n\nAssistant:",
}

// desanitizeMatchString applies providerSanitizationMap and returns both
// the rewritten string and the list of substitutions that fired, so the
// caller can apply the same substitutions to new_string before writing.
// Without the second leg, the model would unwittingly write `<fnr>` etc.
// back into the file every time it touched a context block.
func desanitizeMatchString(s string) (string, [][2]string) {
	out := s
	var applied [][2]string
	for from, to := range providerSanitizationMap {
		next := strings.ReplaceAll(out, from, to)
		if next != out {
			applied = append(applied, [2]string{from, to})
			out = next
		}
	}
	return out, applied
}

// applyDesanitizations re-runs the recorded substitutions against any
// string (typically new_string) so the file's expanded form is preserved
// throughout the edit.
func applyDesanitizations(s string, applied [][2]string) string {
	for _, sub := range applied {
		s = strings.ReplaceAll(s, sub[0], sub[1])
	}
	return s
}

// resolveOldString runs the full normalisation chain on (oldString,
// newString) against content. It returns the (possibly rewritten) pair
// plus a bool saying whether any rewriting happened, so callers can short-
// circuit when nothing needed to change. The returned oldString matches
// content's actual bytes; the returned newString has had the same fixups
// applied so writing it back does not regress the file's typography or
// re-introduce sanitized tokens.
func resolveOldString(content, oldString, newString string) (string, string, bool) {
	if oldString == "" {
		return oldString, newString, false
	}
	if strings.Contains(content, oldString) {
		return oldString, newString, false
	}
	// Quote-insensitive fallback.
	if actual := findActualString(content, oldString); actual != "" {
		return actual, preserveQuoteStyle(oldString, actual, newString), true
	}
	// Provider-sanitised token fallback.
	desanitized, applied := desanitizeMatchString(oldString)
	if len(applied) > 0 && strings.Contains(content, desanitized) {
		return desanitized, applyDesanitizations(newString, applied), true
	}
	return oldString, newString, false
}

// mapNormalizedIndex returns the byte offset in s that corresponds to
// position normIdx in normalizeQuotes(s). Used when a normalised search hit
// at offset N — we need to walk back to the equivalent offset in the
// original (un-normalised) string. Returns -1 when normIdx is past the
// string's normalised length.
func mapNormalizedIndex(s string, normIdx int) int {
	if normIdx == 0 {
		return 0
	}
	consumed := 0
	for i, r := range s {
		// Each curly quote contributes 1 byte after normalisation; other
		// runes contribute utf8.RuneLen(r) bytes.
		var normLen int
		switch r {
		case leftSingleCurlyQuote, rightSingleCurlyQuote,
			leftDoubleCurlyQuote, rightDoubleCurlyQuote:
			normLen = 1
		default:
			normLen = len(string(r))
		}
		if consumed+normLen > normIdx {
			return i
		}
		consumed += normLen
		if consumed == normIdx {
			return i + len(string(r))
		}
	}
	return len(s)
}

// preserveQuoteStyle reapplies the file's curly-quote style to newString
// when oldString matched via quote normalisation. Without this, replacing
// `"foo"` (straight) into a file that uses `"foo"` (curly) would silently
// downgrade the file's typography to ASCII.
//
// Heuristic for double quotes: a `"` preceded by whitespace / start of
// string / opening punctuation is treated as opening; otherwise closing.
//
// For single quotes the same rule applies, except an apostrophe surrounded
// by letters (don't, it's) is left as the right-single-curly-quote to keep
// contractions readable.
func preserveQuoteStyle(origOld, actualOld, newString string) string {
	if origOld == actualOld {
		return newString
	}
	hasDoubleCurly := strings.ContainsAny(actualOld, "“”")
	hasSingleCurly := strings.ContainsAny(actualOld, "‘’")
	if !hasDoubleCurly && !hasSingleCurly {
		return newString
	}
	out := newString
	if hasDoubleCurly {
		out = applyCurlyDouble(out)
	}
	if hasSingleCurly {
		out = applyCurlySingle(out)
	}
	return out
}

func applyCurlyDouble(s string) string {
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range runes {
		if r == '"' {
			if isOpeningContext(runes, i) {
				b.WriteRune(leftDoubleCurlyQuote)
			} else {
				b.WriteRune(rightDoubleCurlyQuote)
			}
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func applyCurlySingle(s string) string {
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	for i, r := range runes {
		if r == '\'' {
			var prev, next rune
			if i > 0 {
				prev = runes[i-1]
			}
			if i+1 < len(runes) {
				next = runes[i+1]
			}
			if unicode.IsLetter(prev) && unicode.IsLetter(next) {
				// don't, it's — contraction; use right-single-curly
				b.WriteRune(rightSingleCurlyQuote)
				continue
			}
			if isOpeningContext(runes, i) {
				b.WriteRune(leftSingleCurlyQuote)
			} else {
				b.WriteRune(rightSingleCurlyQuote)
			}
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isOpeningContext(runes []rune, i int) bool {
	if i == 0 {
		return true
	}
	prev := runes[i-1]
	switch prev {
	case ' ', '\t', '\n', '\r', '(', '[', '{', '—', '–':
		return true
	}
	return false
}

// stripTrailingWhitespace removes trailing spaces and tabs from every line
// while preserving the line ending used in the input (CRLF / LF / CR are
// all detected). Markdown's "two trailing spaces = hard line break" rule
// means we must NOT call this on .md/.mdx files.
func stripTrailingWhitespace(s string) string {
	if s == "" {
		return s
	}
	// Walk the string splitting on line endings without losing them. We
	// scan rune-by-byte to keep this O(n) and avoid the regex tax on hot
	// edit paths.
	var b strings.Builder
	b.Grow(len(s))
	lineStart := 0
	i := 0
	for i < len(s) {
		c := s[i]
		if c == '\n' {
			b.WriteString(strings.TrimRight(s[lineStart:i], " \t"))
			b.WriteByte('\n')
			i++
			lineStart = i
			continue
		}
		if c == '\r' {
			b.WriteString(strings.TrimRight(s[lineStart:i], " \t"))
			if i+1 < len(s) && s[i+1] == '\n' {
				b.WriteString("\r\n")
				i += 2
			} else {
				b.WriteByte('\r')
				i++
			}
			lineStart = i
			continue
		}
		i++
	}
	// Trailing remainder (no final newline).
	b.WriteString(strings.TrimRight(s[lineStart:], " \t"))
	return b.String()
}

// shouldStripTrailingWhitespace reports whether the file at filePath is one
// where stripTrailingWhitespace is safe to apply. Markdown files use two
// trailing spaces as a hard line break (CommonMark §6.7), so stripping
// would silently rewrite layout. Anything else is fair game.
func shouldStripTrailingWhitespace(filePath string) bool {
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".md", ".mdx", ".markdown":
		return false
	}
	return true
}
