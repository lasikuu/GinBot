// Package urlnorm canonicalises URLs to a stable identity for WANHA repost
// detection (W7). Everything here is a pure function: no network access, no
// database, nothing that can fail for a reason other than the input itself.
//
// Two stages, matching docs/plans/wanha.md:
//
//  1. Generic normalisation — lowercase scheme and host, strip a leading
//     www., drop the fragment and default port, strip tracking parameters,
//     and sort what survives so ordering cannot defeat matching.
//  2. A declarative per-host rule table that extracts a canonical source and
//     id, e.g. youtube.com/watch?v=X and youtu.be/X both become
//     "youtube:X". Anything not in the table — or that IS in the table but
//     whose path does not match its extraction pattern — falls back to the
//     fully normalised URL itself as its own identity, rather than guessing
//     at an id and risking a false match.
package urlnorm

import (
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// ErrExcluded reports a URL that must never be indexed: the bot's own web
// URL, or a platform message deep link. Quoting a message must never flag it
// as a repost of itself.
var ErrExcluded = errors.New("url is excluded from repost indexing")

// ErrUnsupported reports input that is not an absolute http or https URL:
// a relative reference, an unknown scheme, a missing host, or a URL carrying
// userinfo (which is never legitimate on the platform CDNs or link
// destinations this exists to canonicalise).
var ErrUnsupported = errors.New("url is not an absolute http or https url")

// Result is a canonicalised URL.
type Result struct {
	// Source is the canonical source name, e.g. "youtube". "url" for the
	// fallback, when no host rule matched or the matched rule's pattern did
	// not fit the path.
	Source string
	// ID is the extracted identifier. For the fallback source it is the
	// fully normalised URL.
	ID string
	// SourceKey is Source + ":" + ID. This is the value that gets indexed.
	SourceKey string
	// CanonicalURL is the fully normalised URL, kept for display.
	CanonicalURL string
}

// Canonicaliser applies the normalisation rules.
type Canonicaliser struct {
	// excludedHosts is checked in addition to the platform message deep
	// links that are always excluded. Matching is case-insensitive and also
	// matches subdomains of each entry.
	excludedHosts map[string]struct{}
}

// New returns a Canonicaliser. extraExcludedHosts are additional hosts that
// must never be indexed — the bot's own web URL, typically.
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

// defaultCanonicaliser backs the package-level Canonicalise, for callers that
// have no extra exclusions of their own (tests, and anything not wired to
// the server's configured web URL).
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

	// The host rule is resolved BEFORE the query is normalised, because which
	// parameters count as tracking depends on whether this is a host we have a
	// rule for (see isTrackingParam). canonicalizeGeneric therefore leaves
	// RawQuery alone and it is set here.
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
		// The host is one we know, but its path did not match the pattern we
		// expect from it (a share link shape we have not seen, a bare
		// homepage URL, and so on). Falling through to the generic "url"
		// source rather than guessing at an id: a wrong id is a false
		// positive, exactly what this whole redesign exists to avoid.
	}

	return Result{
		Source:       "url",
		ID:           canonicalURL,
		SourceKey:    "url:" + canonicalURL,
		CanonicalURL: canonicalURL,
	}, nil
}

// isExcluded reports whether normalized's host is, or is a subdomain of, an
// entry in c.excludedHosts.
func (c *Canonicaliser) isExcluded(normalized *url.URL) bool {
	host := normalized.Hostname()
	for excluded := range c.excludedHosts {
		if host == excluded || strings.HasSuffix(host, "."+excluded) {
			return true
		}
	}
	return false
}

// discordDeepLinkHosts are Discord's own domains and its PTB/canary builds.
// A message link on any of them has the shape /channels/<guild>/<channel>/<message>.
var discordDeepLinkHosts = map[string]struct{}{
	"discord.com":        {},
	"discordapp.com":     {},
	"ptb.discord.com":    {},
	"canary.discord.com": {},
}

// isPlatformDeepLink reports whether normalized points back into a chat
// platform's own message-reference format, rather than at external content.
// Quoting or linking a message must never make it look like a repost of
// itself.
func isPlatformDeepLink(normalized *url.URL) bool {
	host := normalized.Hostname()

	if _, ok := discordDeepLinkHosts[host]; ok && strings.HasPrefix(normalized.Path, "/channels/") {
		return true
	}

	return host == "matrix.to"
}

// canonicalizeGeneric applies stage 1: scheme/host case, www-stripping,
// fragment and default-port removal, trailing-slash and tracking-parameter
// stripping, and query sorting.
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
		// Left RAW on purpose: Canonicalise normalises it once the per-host
		// rule is known, since that decides which parameters are tracking.
		RawQuery: u.RawQuery,
		// Fragment deliberately omitted: it never carries identity for a
		// canonicalised URL and two links differing only in fragment must
		// compare equal.
	}, nil
}

// trackingParamAlways is stripped on every host. Each of these carries only
// campaign or referrer attribution and never identity, so removing one can
// never merge two distinct pages.
var trackingParamAlways = map[string]struct{}{
	"si":      {},
	"igshid":  {},
	"fbclid":  {},
	"gclid":   {},
	"ref_src": {},
	// Acceptance criterion #5 names `ref` explicitly, and it is a referrer tag
	// by near-universal convention. Two referral links to the same page SHOULD
	// converge, so unlike `s` and `t` below this one carries no identity.
	"ref": {},
	// Discord CDN links carry a signed, EXPIRING triple. Two posts of the
	// identical image get different signatures, so leaving these in means the
	// URL never matches itself and the row is dead weight in the index. They
	// are pure request authentication, never identity.
	"ex": {},
	"is": {},
	"hm": {},
}

// trackingParamKnownHost is stripped ONLY when a per-host rule matched.
//
// These names are ambiguous: they are tracking parameters on the big social
// hosts and load-bearing identity anywhere else. `t` is a YouTube timestamp but
// a phpBB topic id; `s` is a Twitter share tag but a WordPress search query.
// Stripping them unconditionally collapses genuinely different pages onto one
// source_key — viewtopic.php?t=100 and ?t=200 become the same key, as do
// search?s=cats and search?s=dogs — which is a WANHA on unrelated content.
// That is precisely the false-positive class this redesign exists to eliminate,
// and it is why docs/plans/wanha.md W7 hedged the rule as "s, t (where
// positional-time semantics do not apply)".
//
// On a host that DID match a rule the risk is absent, because the id comes
// from the path or from a named parameter and the rest of the query is
// discarded anyway — so stripping there costs nothing and keeps the canonical
// URL tidy.
var trackingParamKnownHost = map[string]struct{}{
	"s":              {},
	"t":              {},
	"feature":        {},
	"app":            {},
	"is_from_webapp": {},
	"sender_device":  {},
}

// isTrackingParam reports whether key must be stripped. knownHost is true when
// a per-host rule matched, which widens the set — see trackingParamKnownHost.
//
// Comparison is case-sensitive: query parameter names are, and treating
// "UTM_source" as unrelated to "utm_source" is the safer failure — worst case
// a tracking param survives, which cannot cause a false match on its own since
// the underlying id is unaffected either way.
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

// queryPair is one surviving key/value pair, kept as a pair rather than in a
// url.Values map so it can be sorted by key THEN value — url.Values.Encode
// only sorts by key.
type queryPair struct {
	key   string
	value string
}

// normalizedQuery strips tracking parameters from rawQuery and returns the
// remainder sorted by key, then by value, so that two links differing only in
// parameter order compare equal. knownHost widens which parameters count as
// tracking — see isTrackingParam.
//
// A malformed query is not treated as an error: url.ParseQuery still returns
// whatever it could decode, and best-effort canonicalisation of a slightly
// malformed URL beats refusing to canonicalise it at all.
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
