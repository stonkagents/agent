// Package: tracker/internal/models
// Purpose: Free text rules for the community board (post titles and bodies, reply bodies,
//          report notes, tags, the legacy token offer label), applied server-side on every
//          write. The same idea as SanitizeDisplayName, kept a little more permissive for
//          bodies: code such as "a < b" or "Vec<T>" stays, HTML tags and script blocks go.

package models

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Board text caps in characters (runes), enforced server-side on every write.
const (
	// MaxBoardBodyRunes caps a post body or a reply body (the portal caps at 2000 for typed
	// text; the daemon's autopilot drafts and digests are longer).
	MaxBoardBodyRunes = 10000
	// MaxBoardTitleRunes caps a post title (the column holds 512).
	MaxBoardTitleRunes = 200
	// MaxReportNoteRunes caps the optional note on a report.
	MaxReportNoteRunes = 500
	// MaxBoardTags / MaxBoardTagRunes bound the tags of a post.
	MaxBoardTags     = 10
	MaxBoardTagRunes = 32
	// MaxTokenOfferLabelRunes caps the legacy free-text token offer label ("$STNK").
	MaxTokenOfferLabelRunes = 20
)

// htmlTagNames are the element names a "<name ...>" or "</name>" sequence is stripped for.
// Anything else in angle brackets (generics, comparisons, "<-" channels) is content.
var htmlTagNames = map[string]bool{
	"a": true, "abbr": true, "applet": true, "area": true, "article": true, "aside": true, "audio": true,
	"b": true, "base": true, "bdi": true, "bdo": true, "blockquote": true, "body": true, "br": true, "button": true,
	"canvas": true, "caption": true, "cite": true, "code": true, "col": true, "colgroup": true,
	"data": true, "datalist": true, "dd": true, "del": true, "details": true, "dfn": true, "dialog": true, "div": true, "dl": true, "dt": true,
	"em": true, "embed": true, "fieldset": true, "figcaption": true, "figure": true, "font": true, "footer": true, "form": true, "frame": true, "frameset": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "head": true, "header": true, "hr": true, "html": true,
	"i": true, "iframe": true, "img": true, "input": true, "ins": true, "kbd": true, "label": true, "legend": true, "li": true, "link": true,
	"main": true, "map": true, "mark": true, "marquee": true, "math": true, "menu": true, "meta": true, "meter": true,
	"nav": true, "noscript": true, "object": true, "ol": true, "optgroup": true, "option": true, "output": true,
	"p": true, "param": true, "picture": true, "pre": true, "progress": true, "q": true, "rp": true, "rt": true, "ruby": true,
	"s": true, "samp": true, "script": true, "section": true, "select": true, "slot": true, "small": true, "source": true, "span": true,
	"strike": true, "strong": true, "style": true, "sub": true, "summary": true, "sup": true, "svg": true,
	"table": true, "tbody": true, "td": true, "template": true, "textarea": true, "tfoot": true, "th": true, "thead": true, "time": true, "title": true, "tr": true, "track": true, "tt": true,
	"u": true, "ul": true, "var": true, "video": true, "wbr": true, "xmp": true,
}

// htmlTag matches one tag-like sequence (the name right after "<" or "</", as browsers parse
// it, so "a < b" is never a tag); the name is checked against htmlTagNames.
var htmlTag = regexp.MustCompile(`(?is)</?([a-zA-Z][a-zA-Z0-9-]*)(?:\s[^<>]*)?/?\s*>`)

// htmlBlock matches a script or style element with everything inside it, and comments.
var htmlBlock = regexp.MustCompile(`(?is)<(script|style)\b[^>]*>.*?</(script|style)\s*>|<!--.*?-->`)

// isInvisibleFormat reports the format characters used for spoofing: zero widths, bidi
// controls and the byte order mark. Ordinary combining marks (accents, emoji joiners in
// ZWJ sequences are kept: U+200D is not in the set) stay.
func isInvisibleFormat(r rune) bool {
	switch {
	case r == 0x200B, r == 0x200C, r == 0x200E, r == 0x200F: // zero width space and non-joiner, left/right marks
		return true
	case r >= 0x202A && r <= 0x202E: // bidi embeddings and overrides
		return true
	case r >= 0x2060 && r <= 0x2064, r >= 0x2066 && r <= 0x2069: // word joiner, invisible operators, isolates
		return true
	case r == 0xFEFF: // byte order mark
		return true
	}
	return false
}

// SanitizeBoardText makes s safe to store and show: invalid UTF-8 bytes are dropped, line
// endings become "\n", control characters other than newline and tab go, so do invisible
// format characters (zero widths, bidi overrides), HTML comments, script and style blocks and
// tags whose name is an HTML element; whitespace is trimmed. Emoji and every other script are
// untouched. The result is capped at maxRunes (0 = no cap), never cut inside a character.
func SanitizeBoardText(s string, maxRunes int) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = htmlBlock.ReplaceAllString(s, "")
	s = htmlTag.ReplaceAllStringFunc(s, func(tag string) string {
		name := strings.ToLower(htmlTag.FindStringSubmatch(tag)[1])
		if htmlTagNames[name] {
			return ""
		}
		return tag
	})
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) || isInvisibleFormat(r) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if maxRunes > 0 && utf8.RuneCountInString(s) > maxRunes {
		s = strings.TrimSpace(string([]rune(s)[:maxRunes]))
	}
	return s
}

// BoardTextTooLong reports whether s has more than maxRunes characters.
func BoardTextTooLong(s string, maxRunes int) bool {
	return utf8.RuneCountInString(s) > maxRunes
}

// SanitizeBoardLine is SanitizeBoardText for a single-line value (a title, a tag, a label):
// newlines and tabs collapse into single spaces as well.
func SanitizeBoardLine(s string, maxRunes int) string {
	s = SanitizeBoardText(s, 0)
	s = strings.Join(strings.Fields(s), " ")
	if maxRunes > 0 && utf8.RuneCountInString(s) > maxRunes {
		s = strings.TrimSpace(string([]rune(s)[:maxRunes]))
	}
	return s
}
