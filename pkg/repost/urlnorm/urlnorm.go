// Package urlnorm canonicalises URLs to a stable identity, in two pure stages:
// generic normalisation (case, www., fragment, default port, tracking
// parameters, query order), then a per-host rule table mapping e.g. both
// youtube.com/watch?v=X and youtu.be/X to "youtube:X". Anything unmatched
// falls back to the normalised URL as its own identity rather than guessing.
package urlnorm

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// ErrExcluded reports a URL that must never be indexed: an excluded host or a
// platform message deep link.
var ErrExcluded = errors.New("url is excluded from repost indexing")

// ErrUnsupported reports input that is not an absolute http or https URL, or
// that carries userinfo.
var ErrUnsupported = errors.New("url is not an absolute http or https url")

// Result is a canonicalised URL.
type Result struct {
	// Source is the canonical source name, e.g. "youtube", or "url" for the
	// fallback.
	Source string
	// ID is the extracted identifier, or the normalised URL for the fallback.
	ID string
	// SourceKey is Source + ":" + ID, and is the value that gets indexed.
	SourceKey string
	// CanonicalURL is the fully normalised URL, kept for display.
	CanonicalURL string
}

// Canonicaliser applies the normalisation rules.
type Canonicaliser struct {
	// excludedHosts is matched case-insensitively, and also matches
	// subdomains of each entry.
	excludedHosts map[string]struct{}
}

// New returns a Canonicaliser excluding extraExcludedHosts on top of the
// built-in platform deep links.
func New(extraExcludedHosts []string) *Canonicaliser {
	excluded := make(map[string]struct{}, len(extraExcludedHosts))
	for _, host := range extraExcludedHosts {
		host = strings.ToLower(strings.TrimSpace(host))
		host = strings.TrimPrefix(host, "www.")
		if host == "" {
			continue
		}
		excluded[host] = struct{}{}
	}

	return &Canonicaliser{excludedHosts: excluded}
}

var defaultCanonicaliser = New(nil)

// Canonicalise normalises rawURL using only the built-in exclusions.
func Canonicalise(rawURL string) (Result, error) {
	return defaultCanonicaliser.Canonicalise(rawURL)
}

// Canonicalise normalises rawURL to a stable identity.
func (c *Canonicaliser) Canonicalise(rawURL string) (Result, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrUnsupported, err)
	}

	normalized, err := canonicalizeGeneric(parsed)
	if err != nil {
		return Result{}, err
	}

	if c.isExcluded(normalized) || isPlatformDeepLink(normalized) {
		return Result{}, ErrExcluded
	}

	// Order matters: which parameters count as tracking depends on whether a
	// host rule matched, so canonicalizeGeneric leaves RawQuery alone.
	hs, knownHost := matchHostSource(normalized)
	normalized.RawQuery = normalizedQuery(normalized.RawQuery, knownHost)

	canonicalURL := normalized.String()

	if knownHost {
		if id, ok := hs.extract(normalized); ok && id != "" {
			return Result{
				Source:       hs.source,
				ID:           id,
				SourceKey:    hs.source + ":" + id,
				CanonicalURL: canonicalURL,
			}, nil
		}
		// Known host, unrecognised path shape: fall through to "url" rather
		// than guess an id, since a wrong id is a false positive.
	}

	return Result{
		Source:       "url",
		ID:           canonicalURL,
		SourceKey:    "url:" + canonicalURL,
		CanonicalURL: canonicalURL,
	}, nil
}

func (c *Canonicaliser) isExcluded(normalized *url.URL) bool {
	host := normalized.Hostname()
	for excluded := range c.excludedHosts {
		if host == excluded || strings.HasSuffix(host, "."+excluded) {
			return true
		}
	}
	return false
}

// discordDeepLinkHosts serve message links shaped
// /channels/<guild>/<channel>/<message>.
var discordDeepLinkHosts = map[string]struct{}{
	"discord.com":        {},
	"discordapp.com":     {},
	"ptb.discord.com":    {},
	"canary.discord.com": {},
}

// isPlatformDeepLink reports whether normalized points at a chat platform's own
// message reference, which must never count as a repost of itself.
func isPlatformDeepLink(normalized *url.URL) bool {
	host := normalized.Hostname()

	if _, ok := discordDeepLinkHosts[host]; ok && strings.HasPrefix(normalized.Path, "/channels/") {
		return true
	}

	return host == "matrix.to"
}

// canonicalizeGeneric applies stage 1: scheme/host case, www-stripping,
// fragment and default-port removal, and trailing-slash stripping.
func canonicalizeGeneric(u *url.URL) (*url.URL, error) {
	if !u.IsAbs() || u.Host == "" || u.User != nil {
		return nil, ErrUnsupported
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return nil, ErrUnsupported
	}

	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(host, "www.")

	port := u.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	hostPort := host
	if port != "" {
		hostPort = host + ":" + port
	}

	path := u.Path
	switch {
	case path == "/":
		path = ""
	case len(path) > 1 && strings.HasSuffix(path, "/"):
		path = strings.TrimSuffix(path, "/")
	}

	return &url.URL{
		Scheme: scheme,
		Host:   hostPort,
		Path:   path,
		// Left raw: Canonicalise normalises it once the host rule is known.
		RawQuery: u.RawQuery,
		// Fragment omitted: it never carries identity.
	}, nil
}

// trackingParamAlways is stripped on every host: campaign or referrer
// attribution only, so stripping one can never merge two distinct pages.
var trackingParamAlways = map[string]struct{}{
	"si":      {},
	"igshid":  {},
	"fbclid":  {},
	"gclid":   {},
	"ref_src": {},
	// A referrer tag by near-universal convention, so unlike `s` and `t` it
	// carries no identity.
	"ref": {},
	// Discord CDN's signed, expiring triple: request authentication, never
	// identity, and different on every post of the same image.
	"ex": {},
	"is": {},
	"hm": {},
}

// trackingParamKnownHost is stripped ONLY when a per-host rule matched: these
// names are tracking on the big social hosts but identity elsewhere (`t` is a
// phpBB topic id, `s` a WordPress search query), so a blanket strip would
// collapse unrelated pages onto one source_key.
var trackingParamKnownHost = map[string]struct{}{
	"s":              {},
	"t":              {},
	"feature":        {},
	"app":            {},
	"is_from_webapp": {},
	"sender_device":  {},
}

// isTrackingParam reports whether key must be stripped; knownHost widens the
// set. Comparison is case-sensitive, as query parameter names are.
func isTrackingParam(key string, knownHost bool) bool {
	if strings.HasPrefix(key, "utm_") {
		return true
	}
	if _, ok := trackingParamAlways[key]; ok {
		return true
	}
	if !knownHost {
		return false
	}
	_, ok := trackingParamKnownHost[key]
	return ok
}

// queryPair is a pair rather than a url.Values entry so it can be sorted by
// key then value; url.Values.Encode only sorts by key.
type queryPair struct {
	key   string
	value string
}

// normalizedQuery strips tracking parameters and sorts the rest by key then
// value. A malformed query is decoded best-effort rather than rejected.
func normalizedQuery(rawQuery string, knownHost bool) string {
	parsed, _ := url.ParseQuery(rawQuery)
	if len(parsed) == 0 {
		return ""
	}

	pairs := make([]queryPair, 0, len(parsed))
	for key, values := range parsed {
		if isTrackingParam(key, knownHost) {
			continue
		}
		for _, value := range values {
			pairs = append(pairs, queryPair{key: key, value: value})
		}
	}
	if len(pairs) == 0 {
		return ""
	}

	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].key != pairs[j].key {
			return pairs[i].key < pairs[j].key
		}
		return pairs[i].value < pairs[j].value
	})

	sorted := url.Values{}
	for _, pair := range pairs {
		sorted.Add(pair.key, pair.value)
	}

	return sorted.Encode()
}
