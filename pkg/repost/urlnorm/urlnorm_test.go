package urlnorm

import (
	"errors"
	"testing"
)

// ── Assumed symbols from pkg/repost/urlnorm (spec §3.3) ──────────────────────
//
// Recorded because these are the symbols the tests below depend on, so a change
// to any of them is a deliberate decision rather than a surprise.
//
//	var ErrExcluded = errors.New("url is excluded from repost indexing")
//	var ErrUnsupported = errors.New("url is not an absolute http or https url")
//
//	type Result struct {
//		Source       string
//		ID           string
//		SourceKey    string
//		CanonicalURL string
//	}
//
//	type Canonicaliser struct { /* unexported */ }
//
//	func New(extraExcludedHosts []string) *Canonicaliser
//	func (c *Canonicaliser) Canonicalise(rawURL string) (Result, error)
//	func Canonicalise(rawURL string) (Result, error)
//	func ExtractURLs(text string) []string

// ── Stage 1: generic normalisation ───────────────────────────────────────────

// TestCanonicaliseGenericNormalisation covers stage 1 in isolation, using
// fallback (unmatched) hosts so only the generic rules are in play: lowercase
// scheme/host, drop "www.", drop the fragment, drop the default port, drop a
// trailing slash from a non-empty path, drop an empty query, strip tracking
// parameters, and sort surviving parameters by key then value.
func TestCanonicaliseGenericNormalisation(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantURL    string
		wantSource string
		wantID     string
	}{
		{
			name:       "host is lowercased",
			input:      "https://EXAMPLE.com/Foo",
			wantURL:    "https://example.com/Foo",
			wantSource: "url",
			wantID:     "https://example.com/Foo",
		},
		{
			name:       "scheme is lowercased",
			input:      "HTTPS://example.com/foo",
			wantURL:    "https://example.com/foo",
			wantSource: "url",
			wantID:     "https://example.com/foo",
		},
		{
			name:       "leading www is dropped",
			input:      "https://www.example.com/foo",
			wantURL:    "https://example.com/foo",
			wantSource: "url",
			wantID:     "https://example.com/foo",
		},
		{
			name:       "fragment is dropped",
			input:      "https://example.com/foo#section-2",
			wantURL:    "https://example.com/foo",
			wantSource: "url",
			wantID:     "https://example.com/foo",
		},
		{
			name:       "default https port is dropped",
			input:      "https://example.com:443/foo",
			wantURL:    "https://example.com/foo",
			wantSource: "url",
			wantID:     "https://example.com/foo",
		},
		{
			name:       "non-default port is kept",
			input:      "https://example.com:8443/foo",
			wantURL:    "https://example.com:8443/foo",
			wantSource: "url",
			wantID:     "https://example.com:8443/foo",
		},
		{
			name:       "default http port is dropped",
			input:      "http://example.com:80/foo",
			wantURL:    "http://example.com/foo",
			wantSource: "url",
			wantID:     "http://example.com/foo",
		},
		{
			name:       "trailing slash is dropped from a non-empty path",
			input:      "https://example.com/foo/",
			wantURL:    "https://example.com/foo",
			wantSource: "url",
			wantID:     "https://example.com/foo",
		},
		{
			name:       "empty query is dropped",
			input:      "https://example.com/foo?",
			wantURL:    "https://example.com/foo",
			wantSource: "url",
			wantID:     "https://example.com/foo",
		},
		{
			name:       "surviving parameters are sorted by key",
			input:      "https://example.com/foo?b=2&a=1",
			wantURL:    "https://example.com/foo?a=1&b=2",
			wantSource: "url",
			wantID:     "https://example.com/foo?a=1&b=2",
		},
		{
			name:       "surviving parameters with the same key are sorted by value",
			input:      "https://example.com/foo?tag=b&tag=a",
			wantURL:    "https://example.com/foo?tag=a&tag=b",
			wantSource: "url",
			wantID:     "https://example.com/foo?tag=a&tag=b",
		},
		{
			name:       "a single utm_ parameter is stripped",
			input:      "https://example.com/foo?utm_source=newsletter",
			wantURL:    "https://example.com/foo",
			wantSource: "url",
			wantID:     "https://example.com/foo",
		},
		{
			// AC5 names utm_*, si, igshid, fbclid, gclid and ref. Every one of
			// those is pure attribution and is stripped on ANY host.
			name: "every acceptance-criterion tracking parameter is stripped on an unknown host",
			input: "https://example.com/foo?utm_source=a&utm_medium=b&utm_campaign=c&si=1&igshid=2&" +
				"fbclid=3&gclid=4&ref=5&ref_src=6&keep=yes",
			wantURL:    "https://example.com/foo?keep=yes",
			wantSource: "url",
			wantID:     "https://example.com/foo?keep=yes",
		},
		{
			// The other half of the split: on a host with no per-host rule, `s`
			// and `t` and friends are IDENTITY, not tracking, so they survive.
			// Stripping them here is what made two different forum threads or
			// two different search results collapse onto one source_key — a
			// false positive on unrelated content, which is the exact failure
			// class this redesign exists to remove.
			name:       "ambiguous parameters survive on a host with no rule",
			input:      "https://forum.example.com/viewtopic.php?t=100&s=cats&feature=x",
			wantURL:    "https://forum.example.com/viewtopic.php?feature=x&s=cats&t=100",
			wantSource: "url",
			wantID:     "https://forum.example.com/viewtopic.php?feature=x&s=cats&t=100",
		},
		{
			// Discord CDN links are signed and the signature EXPIRES, so the
			// same image posted twice arrives under two different query
			// strings. Left in, the row could never match itself.
			name:       "discord cdn signed parameters are stripped",
			input:      "https://cdn.discordapp.com/attachments/1/2/a.png?ex=abc&is=def&hm=0123",
			wantURL:    "https://cdn.discordapp.com/attachments/1/2/a.png",
			wantSource: "url",
			wantID:     "https://cdn.discordapp.com/attachments/1/2/a.png",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Canonicalise(tt.input)
			if err != nil {
				t.Fatalf("Canonicalise(%q): unexpected error: %v", tt.input, err)
			}
			if got.CanonicalURL != tt.wantURL {
				t.Errorf("CanonicalURL = %q, want %q", got.CanonicalURL, tt.wantURL)
			}
			if got.Source != tt.wantSource {
				t.Errorf("Source = %q, want %q", got.Source, tt.wantSource)
			}
			if got.ID != tt.wantID {
				t.Errorf("ID = %q, want %q", got.ID, tt.wantID)
			}
			if want := tt.wantSource + ":" + tt.wantID; got.SourceKey != want {
				t.Errorf("SourceKey = %q, want %q", got.SourceKey, want)
			}
		})
	}
}

// ── Acceptance criterion 3: YouTube variants converge ────────────────────────

// TestYouTubeVariantsShareOneSourceKey is AC3: youtu.be/X, youtube.com/watch?v=X,
// youtube.com/shorts/X and m.youtube.com/watch?v=X&si=... must all normalise to
// the same source key.
func TestYouTubeVariantsShareOneSourceKey(t *testing.T) {
	const videoID = "dQw4w9WgXcQ"
	const wantKey = "youtube:" + videoID

	inputs := []string{
		"https://youtu.be/" + videoID,
		"https://youtube.com/watch?v=" + videoID,
		"https://www.youtube.com/watch?v=" + videoID,
		"https://youtube.com/shorts/" + videoID,
		"https://m.youtube.com/watch?v=" + videoID + "&si=aBcDeFgHiJ",
		"https://music.youtube.com/watch?v=" + videoID,
		"https://youtube.com/live/" + videoID,
		"https://youtube.com/embed/" + videoID,
		"https://youtube-nocookie.com/embed/" + videoID,
		"https://YOUTUBE.COM/watch?v=" + videoID,
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			got, err := Canonicalise(input)
			if err != nil {
				t.Fatalf("Canonicalise(%q): unexpected error: %v", input, err)
			}
			if got.SourceKey != wantKey {
				t.Errorf("SourceKey = %q, want %q", got.SourceKey, wantKey)
			}
			if got.Source != "youtube" {
				t.Errorf("Source = %q, want youtube", got.Source)
			}
			if got.ID != videoID {
				t.Errorf("ID = %q, want %q", got.ID, videoID)
			}
		})
	}
}

// TestYouTubeUnmatchedPathFallsBackRatherThanGuessing covers the "wrong id is a
// false positive" rule: a youtube.com URL whose path is not one of the
// recognised shapes must not be forced into the youtube extractor with a
// fabricated id. It must fall back to the generic "url" source.
func TestYouTubeUnmatchedPathFallsBackRatherThanGuessing(t *testing.T) {
	input := "https://youtube.com/channel/UCabcdefghijklmnopqrstuv"

	got, err := Canonicalise(input)
	if err != nil {
		t.Fatalf("Canonicalise(%q): unexpected error: %v", input, err)
	}
	if got.Source != "url" {
		t.Errorf("Source = %q, want %q (fallback, not a fabricated youtube id)", got.Source, "url")
	}
}

// ── Acceptance criterion 4: Twitter/X and its proxy front-ends converge ──────

// TestTwitterVariantsShareOneSourceKey is AC4: twitter.com, x.com, fxtwitter.com
// and vxtwitter.com normalise to the same status key. twittpr.com and the
// nitter.* wildcard are exercised too, since the design explicitly calls out
// proxy front-ends as the case that matters in practice.
func TestTwitterVariantsShareOneSourceKey(t *testing.T) {
	const statusID = "1234567890123456789"
	const wantKey = "twitter:" + statusID

	inputs := []string{
		"https://twitter.com/someuser/status/" + statusID,
		"https://www.twitter.com/someuser/status/" + statusID,
		"https://x.com/someuser/status/" + statusID,
		"https://fxtwitter.com/someuser/status/" + statusID,
		"https://vxtwitter.com/someuser/status/" + statusID,
		"https://fixupx.com/someuser/status/" + statusID,
		"https://twittpr.com/someuser/status/" + statusID,
		"https://nitter.net/someuser/status/" + statusID,
		"https://nitter.example.org/someuser/status/" + statusID,
		"https://twitter.com/someuser/status/" + statusID + "?s=20",
	}

	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			got, err := Canonicalise(input)
			if err != nil {
				t.Fatalf("Canonicalise(%q): unexpected error: %v", input, err)
			}
			if got.SourceKey != wantKey {
				t.Errorf("SourceKey = %q, want %q", got.SourceKey, wantKey)
			}
		})
	}
}

// ── Acceptance criterion 5: tracking parameters ──────────────────────────────

// TestTrackingParametersDoNotSurviveOnRealSites is AC5 exercised against real
// per-host rules rather than the generic fallback: si must fall away before a
// YouTube link's v= is read, and utm_* before a Reddit permalink's id is read.
func TestTrackingParametersDoNotSurviveOnRealSites(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantKey string
	}{
		{
			name:    "youtube si is stripped before v is read",
			input:   "https://m.youtube.com/watch?v=dQw4w9WgXcQ&si=cafeBabe123",
			wantKey: "youtube:dQw4w9WgXcQ",
		},
		{
			name:    "reddit utm parameters do not appear in the source key",
			input:   "https://www.reddit.com/r/pics/comments/abc123/some_title/?utm_source=share&utm_medium=ios_app",
			wantKey: "reddit:abc123",
		},
		{
			name:    "twitter fbclid and gclid are stripped",
			input:   "https://twitter.com/someuser/status/42?fbclid=xyz&gclid=abc",
			wantKey: "twitter:42",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Canonicalise(tt.input)
			if err != nil {
				t.Fatalf("Canonicalise(%q): unexpected error: %v", tt.input, err)
			}
			if got.SourceKey != tt.wantKey {
				t.Errorf("SourceKey = %q, want %q", got.SourceKey, tt.wantKey)
			}
		})
	}
}

// ── Acceptance criterion 6: exclusions ───────────────────────────────────────

// TestPlatformMessageDeepLinksAreExcluded is half of AC6: quoting a Discord
// message must not be indexable as a repost, across the documented host
// aliases, but only when the path is actually a message deep link.
func TestPlatformMessageDeepLinksAreExcluded(t *testing.T) {
	excluded := []string{
		"https://discord.com/channels/111/222/333",
		"https://discordapp.com/channels/111/222/333",
		"https://ptb.discord.com/channels/111/222/333",
		"https://canary.discord.com/channels/111/222/333",
		"https://discord.com/channels/111/222",
	}

	for _, input := range excluded {
		t.Run(input, func(t *testing.T) {
			_, err := Canonicalise(input)
			if !errors.Is(err, ErrExcluded) {
				t.Errorf("Canonicalise(%q) err = %v, want ErrExcluded", input, err)
			}
		})
	}
}

// TestDiscordNonChannelsPathIsNotExcluded proves the exclusion above is scoped
// to the message deep-link path and is not "every discord.com URL": an invite
// link is ordinary content and must still be indexable.
func TestDiscordNonChannelsPathIsNotExcluded(t *testing.T) {
	got, err := Canonicalise("https://discord.com/invite/abcdefg")
	if err != nil {
		t.Fatalf("Canonicalise: unexpected error: %v", err)
	}
	if got.Source != "url" {
		t.Errorf("Source = %q, want the generic fallback (not excluded)", got.Source)
	}
}

// TestMatrixDeepLinksAreExcluded is the other half of AC6 for the Matrix
// platform's own deep-link host.
func TestMatrixDeepLinksAreExcluded(t *testing.T) {
	_, err := Canonicalise("https://matrix.to/#/!roomid:example.org/$eventid")
	if !errors.Is(err, ErrExcluded) {
		t.Errorf("err = %v, want ErrExcluded", err)
	}
}

// TestExtraExcludedHostsAndSubdomainsAreExcluded covers New's own contract: the
// bot's own web URL (an operator-supplied host) is excluded, including any
// subdomain of it, case-insensitively.
func TestExtraExcludedHostsAndSubdomainsAreExcluded(t *testing.T) {
	c := New([]string{"MyBot.Example"})

	excluded := []string{
		"https://mybot.example/invite",
		"https://MYBOT.EXAMPLE/invite",
		"https://cdn.mybot.example/asset.png",
		"https://deeply.nested.mybot.example/x",
	}
	for _, input := range excluded {
		t.Run(input, func(t *testing.T) {
			_, err := c.Canonicalise(input)
			if !errors.Is(err, ErrExcluded) {
				t.Errorf("Canonicalise(%q) err = %v, want ErrExcluded", input, err)
			}
		})
	}

	// A host that merely shares a suffix substring, but is not the excluded
	// host or one of its subdomains, must not be excluded.
	if _, err := c.Canonicalise("https://notmybot.example/x"); errors.Is(err, ErrExcluded) {
		t.Error("a host that is not mybot.example or a subdomain of it was excluded")
	}

	// The package-level Canonicalise (no extra exclusions configured) must not
	// exclude a host that is only excluded via an operator-supplied list.
	if _, err := Canonicalise("https://mybot.example/invite"); errors.Is(err, ErrExcluded) {
		t.Error("the package-level Canonicalise excluded a host that is only excluded via New's argument")
	}
}

// ── ErrUnsupported ────────────────────────────────────────────────────────────

// TestUnsupportedURLs covers every documented ErrUnsupported case: non-http(s)
// scheme, a relative URL, an empty host, and userinfo in the URL — the last of
// which is a request-smuggling-adjacent shape that must never be silently
// accepted.
func TestUnsupportedURLs(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"relative url", "/just/a/path"},
		{"non-http(s) scheme", "ftp://example.com/file.zip"},
		{"mailto scheme", "mailto:someone@example.com"},
		{"empty host", "https:///no-host"},
		{"userinfo present", "https://user:pass@example.com/x"},
		{"not a url at all", "this is not a url"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Canonicalise(tt.input)
			if !errors.Is(err, ErrUnsupported) {
				t.Errorf("Canonicalise(%q) err = %v, want ErrUnsupported", tt.input, err)
			}
		})
	}
}

// ── Per-host rule table: Reddit ──────────────────────────────────────────────

func TestRedditVariants(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantKey string
	}{
		{"reddit.com comments permalink", "https://www.reddit.com/r/pics/comments/abc123/a_title/", "reddit:abc123"},
		{"old.reddit.com comments permalink", "https://old.reddit.com/r/pics/comments/abc123/a_title/", "reddit:abc123"},
		{"new.reddit.com comments permalink", "https://new.reddit.com/r/pics/comments/abc123/a_title/", "reddit:abc123"},
		{"np.reddit.com comments permalink", "https://np.reddit.com/r/pics/comments/abc123/a_title/", "reddit:abc123"},
		{"reddit.com share token", "https://www.reddit.com/r/pics/s/AbCdEfGh12", "reddit:AbCdEfGh12"},
		{"redd.it short link", "https://redd.it/abc123", "reddit:abc123"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Canonicalise(tt.input)
			if err != nil {
				t.Fatalf("Canonicalise(%q): unexpected error: %v", tt.input, err)
			}
			if got.SourceKey != tt.wantKey {
				t.Errorf("SourceKey = %q, want %q", got.SourceKey, tt.wantKey)
			}
		})
	}
}

// ── Per-host rule table: Twitch, TikTok, Instagram, Bluesky ──────────────────

func TestTwitchVariants(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantKey string
	}{
		{"clip under a channel", "https://twitch.tv/somechannel/clip/AwesomeSlugName", "twitch:AwesomeSlugName"},
		{"dedicated clip host", "https://clips.twitch.tv/AwesomeSlugName", "twitch:AwesomeSlugName"},
		{"video id", "https://twitch.tv/videos/123456789", "twitch:123456789"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Canonicalise(tt.input)
			if err != nil {
				t.Fatalf("Canonicalise(%q): unexpected error: %v", tt.input, err)
			}
			if got.SourceKey != tt.wantKey {
				t.Errorf("SourceKey = %q, want %q", got.SourceKey, tt.wantKey)
			}
		})
	}
}

func TestTikTokVariants(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantKey string
	}{
		{"canonical video url", "https://www.tiktok.com/@someuser/video/7123456789012345678", "tiktok:7123456789012345678"},
		{"vm share link", "https://vm.tiktok.com/ZMabcdEFG/", "tiktok:ZMabcdEFG"},
		{"vt share link", "https://vt.tiktok.com/ZMabcdEFG/", "tiktok:ZMabcdEFG"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Canonicalise(tt.input)
			if err != nil {
				t.Fatalf("Canonicalise(%q): unexpected error: %v", tt.input, err)
			}
			if got.SourceKey != tt.wantKey {
				t.Errorf("SourceKey = %q, want %q", got.SourceKey, tt.wantKey)
			}
		})
	}
}

func TestInstagramVariants(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantKey string
	}{
		{"post", "https://www.instagram.com/p/AbC123xyz/", "instagram:AbC123xyz"},
		{"reel", "https://instagram.com/reel/AbC123xyz/", "instagram:AbC123xyz"},
		{"tv", "https://instagram.com/tv/AbC123xyz/", "instagram:AbC123xyz"},
		{"proxy front-end", "https://ddinstagram.com/p/AbC123xyz/", "instagram:AbC123xyz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Canonicalise(tt.input)
			if err != nil {
				t.Fatalf("Canonicalise(%q): unexpected error: %v", tt.input, err)
			}
			if got.SourceKey != tt.wantKey {
				t.Errorf("SourceKey = %q, want %q", got.SourceKey, tt.wantKey)
			}
		})
	}
}

// TestBlueskyExtractsHandleAndRkey covers the one source whose id is a compound
// value rather than a single token.
func TestBlueskyExtractsHandleAndRkey(t *testing.T) {
	input := "https://bsky.app/profile/alice.bsky.social/post/3jzfnovewqk2h"
	want := "bluesky:alice.bsky.social/3jzfnovewqk2h"

	got, err := Canonicalise(input)
	if err != nil {
		t.Fatalf("Canonicalise(%q): unexpected error: %v", input, err)
	}
	if got.SourceKey != want {
		t.Errorf("SourceKey = %q, want %q", got.SourceKey, want)
	}
}

// ── Fallback ──────────────────────────────────────────────────────────────────

// TestUnrecognisedHostFallsBackToURL covers the "url" fallback source for any
// host with no per-host rule: the id is the fully normalised URL itself.
func TestUnrecognisedHostFallsBackToURL(t *testing.T) {
	input := "https://some-blog.example/2026/08/23/a-post?utm_source=twitter&keep=me"
	wantURL := "https://some-blog.example/2026/08/23/a-post?keep=me"

	got, err := Canonicalise(input)
	if err != nil {
		t.Fatalf("Canonicalise(%q): unexpected error: %v", input, err)
	}
	if got.Source != "url" {
		t.Errorf("Source = %q, want url", got.Source)
	}
	if got.CanonicalURL != wantURL {
		t.Errorf("CanonicalURL = %q, want %q", got.CanonicalURL, wantURL)
	}
	if got.ID != wantURL {
		t.Errorf("ID = %q, want the canonical URL %q", got.ID, wantURL)
	}
	if got.SourceKey != "url:"+wantURL {
		t.Errorf("SourceKey = %q, want %q", got.SourceKey, "url:"+wantURL)
	}
}

// ── ExtractURLs ───────────────────────────────────────────────────────────────

// TestExtractURLsFindsEveryURLInOrderDeduplicated covers the base case: several
// distinct URLs are returned in appearance order, and an exact duplicate is not
// repeated.
func TestExtractURLsFindsEveryURLInOrderDeduplicated(t *testing.T) {
	text := "check this out https://example.com/a and also http://example.org/b, " +
		"oh and https://example.com/a again"

	got := ExtractURLs(text)
	want := []string{"https://example.com/a", "http://example.org/b"}

	if len(got) != len(want) {
		t.Fatalf("ExtractURLs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ExtractURLs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestExtractURLsStripsTrailingSentencePunctuation covers every documented
// trailing character, including unbalanced closing brackets/quotes that were
// not opened in the URL itself.
func TestExtractURLsStripsTrailingSentencePunctuation(t *testing.T) {
	tests := []struct {
		name string
		text string
		want string
	}{
		{"trailing period", "see https://example.com/a.", "https://example.com/a"},
		{"trailing comma", "see https://example.com/a, please", "https://example.com/a"},
		{"trailing exclamation", "see https://example.com/a!", "https://example.com/a"},
		{"trailing question mark", "see https://example.com/a?", "https://example.com/a"},
		{"trailing colon", "see https://example.com/a:", "https://example.com/a"},
		{"trailing semicolon", "see https://example.com/a;", "https://example.com/a"},
		{"unbalanced closing paren", "(see https://example.com/a)", "https://example.com/a"},
		{"unbalanced closing bracket", "[see https://example.com/a]", "https://example.com/a"},
		{"unbalanced angle bracket", "see https://example.com/a>", "https://example.com/a"},
		{"unbalanced double quote", `see "https://example.com/a"`, "https://example.com/a"},
		{"unbalanced single quote", "see 'https://example.com/a'", "https://example.com/a"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractURLs(tt.text)
			if len(got) != 1 {
				t.Fatalf("ExtractURLs(%q) = %v, want exactly one URL", tt.text, got)
			}
			if got[0] != tt.want {
				t.Errorf("ExtractURLs(%q)[0] = %q, want %q", tt.text, got[0], tt.want)
			}
		})
	}
}

// TestExtractURLsHandlesDiscordSuppressedEmbedForm: wrapping a link in angle
// brackets is Discord's own syntax for "post this link but do not embed it".
// The URL underneath is still a real candidate for repost checking and the
// brackets are not part of it.
func TestExtractURLsHandlesDiscordSuppressedEmbedForm(t *testing.T) {
	text := "no embed please <https://example.com/a>"

	got := ExtractURLs(text)
	if len(got) != 1 || got[0] != "https://example.com/a" {
		t.Errorf("ExtractURLs(%q) = %v, want [\"https://example.com/a\"]", text, got)
	}
}

// TestExtractURLsHandlesSpoilerWrapping: a spoilered link must still be
// checkable — WANHA should not be blind to a repost just because the poster
// marked it as a spoiler.
func TestExtractURLsHandlesSpoilerWrapping(t *testing.T) {
	text := "||https://example.com/a||"

	got := ExtractURLs(text)
	if len(got) != 1 || got[0] != "https://example.com/a" {
		t.Errorf("ExtractURLs(%q) = %v, want [\"https://example.com/a\"]", text, got)
	}
}

// TestExtractURLsReturnsNilForNoURLs: repostCandidates depends on being able to
// tell "nothing to check" apart from "one candidate", so an empty result must
// genuinely be empty rather than a slice of length zero built some other way
// that still evaluates truthy in an unexpected place. len() == 0 covers both.
func TestExtractURLsReturnsNilForNoURLs(t *testing.T) {
	got := ExtractURLs("just some ordinary chat, nothing to see here")
	if len(got) != 0 {
		t.Errorf("ExtractURLs(no urls) = %v, want empty", got)
	}
}

// ── The known-host / unknown-host split in tracking-param stripping ──────────

// TestAmbiguousParametersAreStrippedOnlyOnKnownHosts pins the split that keeps
// AC5 and AC9 from contradicting each other.
//
// `s` and `t` are tracking noise on the social hosts and load-bearing identity
// everywhere else. Stripping them everywhere satisfies AC5's letter but
// reintroduces AC9's false positives; stripping them nowhere leaves YouTube
// timestamps defeating a match. The rule table decides which a host is.
func TestAmbiguousParametersAreStrippedOnlyOnKnownHosts(t *testing.T) {
	t.Run("distinct forum threads stay distinct", func(t *testing.T) {
		first, err := Canonicalise("https://forum.example.com/viewtopic.php?t=100")
		if err != nil {
			t.Fatalf("Canonicalise: %v", err)
		}
		second, err := Canonicalise("https://forum.example.com/viewtopic.php?t=200")
		if err != nil {
			t.Fatalf("Canonicalise: %v", err)
		}
		if first.SourceKey == second.SourceKey {
			t.Errorf("two different forum threads collapsed onto one source_key %q; this is a false positive",
				first.SourceKey)
		}
	})

	t.Run("distinct search queries stay distinct", func(t *testing.T) {
		cats, err := Canonicalise("https://blog.example.com/search?s=cats")
		if err != nil {
			t.Fatalf("Canonicalise: %v", err)
		}
		dogs, err := Canonicalise("https://blog.example.com/search?s=dogs")
		if err != nil {
			t.Fatalf("Canonicalise: %v", err)
		}
		if cats.SourceKey == dogs.SourceKey {
			t.Errorf("two different search results collapsed onto one source_key %q", cats.SourceKey)
		}
	})

	t.Run("a youtube timestamp does not defeat a match", func(t *testing.T) {
		plain, err := Canonicalise("https://www.youtube.com/watch?v=dQw4w9WgXcQ")
		if err != nil {
			t.Fatalf("Canonicalise: %v", err)
		}
		stamped, err := Canonicalise("https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=42s")
		if err != nil {
			t.Fatalf("Canonicalise: %v", err)
		}
		if plain.SourceKey != stamped.SourceKey {
			t.Errorf("timestamped YouTube link keyed %q, plain keyed %q; they must converge",
				stamped.SourceKey, plain.SourceKey)
		}
	})
}

// TestKnownHostPathsThatDoNotMatchTheirRuleDoNotMintAnID guards the wrong-id
// class directly: a marker word appearing at the wrong depth in a path must not
// be mistaken for the marker the rule is looking for. `r/s` is a real
// subreddit, so an unanchored search for "s" turned an arbitrary wiki page into
// a post id.
func TestKnownHostPathsThatDoNotMatchTheirRuleDoNotMintAnID(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"reddit wiki page under the r/s subreddit", "https://reddit.com/r/s/wiki/index"},
		{"twitter status marker with no user segment", "https://twitter.com/status/123"},
		{"tiktok video marker at the wrong depth", "https://tiktok.com/video/123"},
		{"twitch clip marker with no channel segment", "https://twitch.tv/clip/someslug"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Canonicalise(tt.input)
			if err != nil {
				t.Fatalf("Canonicalise(%q): %v", tt.input, err)
			}
			if got.Source != "url" {
				t.Errorf("Source = %q (id %q), want the %q fallback: a shape the rule does not understand must not mint an id",
					got.Source, got.ID, "url")
			}
		})
	}
}
