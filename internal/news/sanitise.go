package news

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

// scriptOrStyle matches <script> and <style> elements including their content,
// which is code rather than readable text.
var scriptOrStyle = regexp.MustCompile(`(?is)<(script|style)\b.*?</(script|style)\s*>`)

// CleanText turns feed text into one plain line: HTML elements are removed,
// entities decoded, and every run of whitespace or control characters
// (newlines, tabs, Unicode line separators) becomes a single space. The
// result is capped at maxRunes runes (0: no cap) on a rune boundary.
func CleanText(text string, maxRunes int) string {
	// Two strip/decode passes: feeds often escape their HTML, so the first
	// decode can reveal a second layer of tags.
	for range 2 {
		text = html.UnescapeString(stripTags(scriptOrStyle.ReplaceAllString(text, " ")))
	}
	return capRunes(collapseSpace(text), maxRunes)
}

// stripTags removes anything that looks like an HTML tag, comment or
// declaration. A '<' not followed by a letter, '/', '!' or '?' is kept so
// text such as "BTC < $100k" survives.
func stripTags(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	runes := []rune(text)
	for i := 0; i < len(runes); i++ {
		if runes[i] == '<' && i+1 < len(runes) && isTagStart(runes[i+1]) {
			end := i + 1
			for end < len(runes) && runes[end] != '>' {
				end++
			}
			if end < len(runes) {
				b.WriteRune(' ') // keep words on either side of the tag apart
				i = end
				continue
			}
		}
		b.WriteRune(runes[i])
	}
	return b.String()
}

// isTagStart reports whether r can follow '<' at the start of an HTML tag.
func isTagStart(r rune) bool {
	return unicode.IsLetter(r) || r == '/' || r == '!' || r == '?'
}

// collapseSpace replaces every run of whitespace or control characters with a
// single space and trims the ends, so the text cannot span several lines.
func collapseSpace(text string) string {
	return strings.Join(strings.FieldsFunc(text, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r)
	}), " ")
}

// capRunes truncates text to at most maxRunes runes (0: unchanged).
func capRunes(text string, maxRunes int) string {
	if maxRunes <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return strings.TrimSpace(string(runes[:maxRunes]))
}

// safeLink returns link when it is an absolute http(s) URL, otherwise "".
func safeLink(link string) string {
	link = strings.TrimSpace(link)
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return ""
	}
	if scheme := strings.ToLower(u.Scheme); scheme != "http" && scheme != "https" {
		return ""
	}
	return link // unchanged, so article IDs stay stable
}
