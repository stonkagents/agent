// Package api: title and body resolution shared by the two create-post routes
// (POST /api/board/posts and POST /api/v1/tracker/forum/posts).
package api

import (
	"errors"
	"strings"

	"github.com/stonkagents/agent/tracker/internal/services"
)

const (
	// PostTitleMaxRunes caps an explicit or derived post title (the column holds 512).
	PostTitleMaxRunes = 200
	// PostTitleFallbackRunes is how much of the body becomes the title when the first line
	// gives nothing usable.
	PostTitleFallbackRunes = 80
)

// ErrPostBodyEmpty is returned when neither body nor its aliases carry any text.
var ErrPostBodyEmpty = errors.New("post body is empty")

// resolvePostTitleAndBody picks the stored title and description of a new post.
//
//   - body is the first non-empty of the given body candidates, in order (`body`, then
//     `content`, then `description`), trimmed; none: ErrPostBodyEmpty.
//   - An explicit title wins: collapsed to one line and capped at PostTitleMaxRunes. When it
//     equals the body's first line (the daemon digest sends "title\n\nbody") that line is
//     dropped from the stored body so the thread does not show the title twice; a body that
//     is only that line is kept as is.
//   - Without one the title is the body's first line, capped the same way; when that line
//     is empty the first PostTitleFallbackRunes of the body serve (never the placeholder
//     "Post").
func resolvePostTitleAndBody(explicitTitle string, bodyCandidates ...string) (title, body string, err error) {
	for _, c := range bodyCandidates {
		if body = strings.TrimSpace(c); body != "" {
			break
		}
	}
	if body == "" {
		return "", "", ErrPostBodyEmpty
	}
	firstLine, rest := body, ""
	if idx := strings.IndexByte(body, '\n'); idx >= 0 {
		firstLine, rest = strings.TrimSpace(body[:idx]), strings.TrimSpace(body[idx+1:])
	}
	title = oneLineTitle(explicitTitle)
	if title != "" {
		if rest != "" && title == oneLineTitle(firstLine) {
			body = rest
		}
		return title, body, nil
	}
	title = oneLineTitle(firstLine)
	if title == "" {
		title = oneLineTitle(services.TruncateRunes(body, PostTitleFallbackRunes))
	}
	if title == "" {
		title = "Post"
	}
	return title, body, nil
}

// oneLineTitle collapses s to a single trimmed line capped at PostTitleMaxRunes.
func oneLineTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	return strings.TrimSpace(services.TruncateRunes(s, PostTitleMaxRunes))
}
