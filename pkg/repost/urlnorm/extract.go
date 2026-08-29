package urlnorm

import (
	"regexp"
	"strings"
)

// urlPattern matches an absolute http or https URL. The excluded characters
// stop the match at Discord's ||spoiler|| bars and <suppressed-embed> brackets.
var urlPattern = regexp.MustCompile(`(?i)https?://[^\s<>|]+`)

// ExtractURLs returns every absolute http or https URL in text, in order of
// appearance, deduplicated by exact string and stripped of trailing sentence
// punctuation.
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
// ')' and ']' only when unbalanced, since ".../Foo_(bar)" legitimately ends in one.
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
