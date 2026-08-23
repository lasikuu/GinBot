package urlnorm

import (
	"regexp"
	"strings"
)

// urlPattern matches an absolute http or https URL. "|" is excluded from the
// body so that Discord's ||spoiler|| wrapping does not get absorbed into the
// match; "<" and ">" are excluded so that Discord's suppressed-embed
// <https://...> form naturally stops the match at the closing bracket without
// any special-casing (the opening bracket is never part of the match to begin
// with, since it precedes the "https://" the pattern anchors on).
var urlPattern = regexp.MustCompile(`(?i)https?://[^\s<>|]+`)

// ExtractURLs returns every absolute http or https URL in text, in order of
// appearance, deduplicated by exact string.
//
// This is deliberately not a naive greedy regexp applied verbatim: sentence
// punctuation immediately after a URL — a period ending a sentence, a comma
// before the next clause, a closing quote — is not part of the URL, and a
// greedy character class would swallow it into the match, corrupting the
// canonical form and any host/path extraction downstream.
func ExtractURLs(text string) []string {
	matches := urlPattern.FindAllString(text, -1)
	if len(matches) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(matches))
	out := make([]string, 0, len(matches))
	for _, match := range matches {
		trimmed := trimTrailingPunctuation(match)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}

	return out
}

// trimTrailingPunctuation strips sentence punctuation trailing a matched URL.
//
// ')' and ']' are only stripped when UNBALANCED within the match — a
// Wikipedia-style URL like ".../Foo_(bar)" legitimately ends with a
// parenthesis, and stripping it unconditionally would corrupt exactly the
// URLs most likely to contain one.
func trimTrailingPunctuation(s string) string {
	for s != "" {
		last := s[len(s)-1]
		switch last {
		case '.', ',', '!', '?', ':', ';', '"', '\'':
			s = s[:len(s)-1]
			continue
		case ')':
			if strings.Count(s, "(") < strings.Count(s, ")") {
				s = s[:len(s)-1]
				continue
			}
		case ']':
			if strings.Count(s, "[") < strings.Count(s, "]") {
				s = s[:len(s)-1]
				continue
			}
		}
		break
	}
	return s
}
