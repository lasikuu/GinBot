package urlnorm

import (
	"net/url"
	"strings"
)

// hostSource is one row of the declarative per-host rule table.
type hostSource struct {
	source string
	// hosts matches the exact, already-www-stripped hostname. Unlisted
	// subdomains fall through to the "url" fallback rather than inheriting
	// their parent's rule.
	hosts map[string]struct{}
	// prefixes matches host families that cannot be enumerated, e.g. the many
	// public nitter instances.
	prefixes []string
	// extract pulls the id out of the path or query; ok is false when the
	// host is known but its URL shape is not, meaning fall back, not guess.
	extract func(u *url.URL) (id string, ok bool)
}

func hostSet(hosts ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(hosts))
	for _, h := range hosts {
		set[h] = struct{}{}
	}
	return set
}

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
			// /r/<sub>/s/<token> — a share link. Known limitation: the token
			// is a different identifier to the post id and resolving it needs
			// a network fetch, so the two shapes do not converge.
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

// matchHostSource finds the rule for u's host, exact match before prefix.
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

// segmentAfterAt returns the path segment following marker, requiring marker
// to sit at exactly index. Anchoring matters: an unanchored search would mint
// an id from reddit.com/r/s/wiki/index, r/s being a real subreddit.
func segmentAfterAt(segments []string, marker string, index int) (string, bool) {
	if index < 0 || index+1 >= len(segments) {
		return "", false
	}
	if segments[index] != marker {
		return "", false
	}
	return segments[index+1], true
}

// extractFirstSegment is the whole identity for hosts whose links are <host>/<id>.
func extractFirstSegment(u *url.URL) (string, bool) {
	segments := pathSegments(u)
	if len(segments) == 0 {
		return "", false
	}
	return segments[0], true
}
