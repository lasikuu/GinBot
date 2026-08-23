package urlnorm

import (
	"net/url"
	"strings"
)

// hostSource is one row of the declarative per-host rule table (W7): adding
// a site is a data change here, not a new code path in Canonicalise.
type hostSource struct {
	source string
	// hosts is matched against the exact, already-www-stripped hostname.
	// Subdomains are deliberately NOT matched generically — a subdomain not
	// explicitly listed (e.g. some other "foo.reddit.com") falls through to
	// the "url" fallback rather than being assumed to behave like its parent.
	hosts map[string]struct{}
	// prefixes matches a hostname by prefix, for a family of hosts that
	// cannot be enumerated (nitter has many public instances).
	prefixes []string
	// extract pulls the id out of the path or query. ok is false when the
	// host is recognised but the URL's shape is not one this rule
	// understands, which means "fall back to the url source", not "guess".
	extract func(u *url.URL) (id string, ok bool)
}

func hostSet(hosts ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		set[h] = struct{}{}
	}
	return set
}

// hostSources is the rule table itself.
var hostSources = []hostSource{
	{
		source: "youtube",
		hosts:  hostSet("youtube.com", "m.youtube.com", "music.youtube.com", "youtube-nocookie.com"),
		extract: func(u *url.URL) (string, bool) {
			if v := u.Query().Get("v"); v != "" {
				return v, true
			}
			segments := pathSegments(u)
			if len(segments) >= 2 {
				switch segments[0] {
				case "shorts", "live", "embed":
					return segments[1], true
				}
			}
			return "", false
		},
	},
	{
		source:  "youtube",
		hosts:   hostSet("youtu.be"),
		extract: extractFirstSegment,
	},
	{
		source:   "twitter",
		hosts:    hostSet("twitter.com", "x.com", "fxtwitter.com", "vxtwitter.com", "fixupx.com", "twittpr.com"),
		prefixes: []string{"nitter."},
		extract: func(u *url.URL) (string, bool) {
			// /<user>/status/<id>
			return segmentAfterAt(pathSegments(u), "status", 1)
		},
	},
	{
		source: "reddit",
		hosts:  hostSet("reddit.com", "old.reddit.com", "new.reddit.com", "np.reddit.com"),
		extract: func(u *url.URL) (string, bool) {
			segments := pathSegments(u)
			// /r/<sub>/comments/<id>/<slug>
			if id, ok := segmentAfterAt(segments, "comments", 2); ok {
				return id, true
			}
			// /r/<sub>/s/<token> — a share link.
			//
			// KNOWN LIMITATION: a share token is a DIFFERENT identifier to the
			// post id, and resolving one to the other needs a network fetch
			// Canonicalise deliberately cannot make (it is a pure function).
			// So reddit.com/r/x/s/AbC and reddit.com/r/x/comments/abc123/... for
			// the same post do NOT converge, contrary to the implication in
			// docs/plans/wanha.md W7 ("post id, incl. /s/ share links").
			// Indexing the token anyway is still worth it — the same share link
			// reposted does match — and the failure mode is a missed detection,
			// never a false one.
			return segmentAfterAt(segments, "s", 2)
		},
	},
	{
		source:  "reddit",
		hosts:   hostSet("redd.it", "i.redd.it", "v.redd.it"),
		extract: extractFirstSegment,
	},
	{
		source: "twitch",
		hosts:  hostSet("twitch.tv"),
		extract: func(u *url.URL) (string, bool) {
			segments := pathSegments(u)
			if len(segments) >= 2 && segments[0] == "videos" {
				return segments[1], true
			}
			// /<channel>/clip/<slug>
			return segmentAfterAt(segments, "clip", 1)
		},
	},
	{
		source:  "twitch",
		hosts:   hostSet("clips.twitch.tv"),
		extract: extractFirstSegment,
	},
	{
		source: "tiktok",
		hosts:  hostSet("tiktok.com"),
		extract: func(u *url.URL) (string, bool) {
			// /@<user>/video/<id>
			return segmentAfterAt(pathSegments(u), "video", 1)
		},
	},
	{
		source:  "tiktok",
		hosts:   hostSet("vm.tiktok.com", "vt.tiktok.com"),
		extract: extractFirstSegment,
	},
	{
		source: "instagram",
		hosts:  hostSet("instagram.com", "ddinstagram.com"),
		extract: func(u *url.URL) (string, bool) {
			segments := pathSegments(u)
			if len(segments) >= 2 {
				switch segments[0] {
				case "p", "reel", "tv":
					return segments[1], true
				}
			}
			return "", false
		},
	},
	{
		source: "bluesky",
		hosts:  hostSet("bsky.app"),
		extract: func(u *url.URL) (string, bool) {
			// /profile/<handle>/post/<rkey>
			segments := pathSegments(u)
			if len(segments) >= 4 && segments[0] == "profile" && segments[2] == "post" {
				return segments[1] + "/" + segments[3], true
			}
			return "", false
		},
	},
}

// matchHostSource finds the rule for u's host, by exact match first and then
// by prefix.
func matchHostSource(u *url.URL) (hostSource, bool) {
	host := u.Hostname()

	for _, hs := range hostSources {
		if _, ok := hs.hosts[host]; ok {
			return hs, true
		}
	}
	for _, hs := range hostSources {
		for _, prefix := range hs.prefixes {
			if strings.HasPrefix(host, prefix) {
				return hs, true
			}
		}
	}

	return hostSource{}, false
}

// pathSegments splits u's path into its non-empty segments.
func pathSegments(u *url.URL) []string {
	trimmed := strings.Trim(u.Path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

// segmentAfterAt returns the path segment following marker, requiring marker to
// sit at exactly index.
//
// The anchor is the point. An unanchored search for the marker anywhere in the
// path mints ids out of paths that merely contain the word: with a free search
// for "s", reddit.com/r/s/wiki/index yields "reddit:wiki" — r/s is a real
// subreddit — so an arbitrary wiki page acquires the id of a post. A wrong id
// is a false positive, and every real URL shape these rules target has its
// marker at a fixed depth anyway.
func segmentAfterAt(segments []string, marker string, index int) (string, bool) {
	if index < 0 || index+1 >= len(segments) {
		return "", false
	}
	if segments[index] != marker {
		return "", false
	}
	return segments[index+1], true
}

// extractFirstSegment returns the first path segment: the whole identity for
// a host whose links are already just <host>/<id>.
func extractFirstSegment(u *url.URL) (string, bool) {
	segments := pathSegments(u)
	if len(segments) == 0 {
		return "", false
	}
	return segments[0], true
}
